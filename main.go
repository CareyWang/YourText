package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/net/context"
)

var (
	MinIOEndpoint string
	AccessKey     string
	SecretKey     string
	BucketName    string
	UseSSL        bool // 是否使用 SSL
)

var AppURL string
var AppPort string

const contentMaxLength = 10000

type Req struct {
	Content string `json:"content" binding:"required"`
}

type Resp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data gin.H  `json:"data"`
}

func init() {
	// 加载 .env 文件
	// 加载失败不影响服务
	godotenv.Load()

	// 从环境变量中获取 AppURL
	AppURL = os.Getenv("YOURTEXT_APP_URL")
	AppPort = os.Getenv("YOURTEXT_APP_PORT")

	// 从环境变量中获取 MinIO 配置
	MinIOEndpoint = os.Getenv("YOURTEXT_MINIO_ENDPOINT")
	AccessKey = os.Getenv("YOURTEXT_MINIO_ACCESS_KEY")
	SecretKey = os.Getenv("YOURTEXT_MINIO_SECRET_KEY")
	BucketName = os.Getenv("YOURTEXT_MINIO_BUCKET_NAME")
	UseSSL = false
}

// Cors 开放所有接口的OPTIONS方法
func Cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method               // 请求方法
		origin := c.Request.Header.Get("Origin") // 请求头部
		var headerKeys []string                  // 声明请求头keys
		for k := range c.Request.Header {
			headerKeys = append(headerKeys, k)
		}
		headerStr := strings.Join(headerKeys, ", ")
		if headerStr != "" {
			headerStr = fmt.Sprintf("access-control-allow-origin, access-control-allow-headers, %s", headerStr)
		} else {
			headerStr = "access-control-allow-origin, access-control-allow-headers"
		}
		if origin != "" {
			origin := c.Request.Header.Get("Origin")
			c.Header("Access-Control-Allow-Origin", origin)                                     // 这是允许访问所有域
			c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE, UPDATE") // 服务器支持的所有跨域请求的方法,为了避免浏览次请求的多次'预检'请求
			// header的类型
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Length, X-CSRF-Token, Token,session,X_Requested_With,Accept, Origin, Host, Connection, Accept-Encoding, Accept-Language,DNT, X-CustomHeader, Keep-Alive, User-Agent, X-Requested-With, If-Modified-Since, Cache-Control, Content-Type, Pragma")
			// 允许跨域设置，可以返回其他子段
			c.Header("Access-Control-Expose-Headers", "Content-Length, Access-Control-Allow-Origin, Access-Control-Allow-Headers,Cache-Control,Content-Language,Content-Type,Expires,Last-Modified,Pragma,FooBar") // 跨域关键设置 让浏览器可以解析
			c.Header("Access-Control-Max-Age", "172800")                                                                                                                                                           // 缓存请求信息 单位为秒
			c.Header("Access-Control-Allow-Credentials", "false")                                                                                                                                                  //  跨域请求是否需要带cookie信息 默认设置为true
			c.Set("content-type", "application/json")                                                                                                                                                              // 设置返回格式是json
		}

		// 放行所有OPTIONS方法
		if method == "OPTIONS" {
			c.JSON(http.StatusOK, "Options Request!")
		}
		// 处理请求
		c.Next() //  处理请求
	}
}

func main() {
	// 初始化 MinIO 客户端
	minioClient, err := initMinIOClient()
	if err != nil {
		panic(err)
	}

	// GC 优化。预分配 1G 内存（不会实际占用）
	ballast := make([]byte, 1<<30)
	defer func() {
		log.Printf("ballast len %v", len(ballast))
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 检查 bucket
	initMinIOBucket(ctx, minioClient)

	r := gin.Default()
	r.Use(Cors())

	// 上传文件
	r.POST("/upload", func(c *gin.Context) {
		resp := &Resp{
			Code: 0,
			Msg:  "",
			Data: gin.H{},
		}

		var data Req
		if err := c.ShouldBindJSON(&data); err != nil {
			resp.Code = 1
			resp.Msg = "Bad request"

			c.JSON(400, resp)
			return
		}
		if len(data.Content) > contentMaxLength {
			resp.Code = 2
			resp.Msg = fmt.Sprintf("content too long, max length is %d", contentMaxLength)

			c.JSON(400, resp)
			return
		}

		// 根据年月日生成路径
		path := time.Now().Format("2006/01/02")

		// 生成随机文件名
		filePath := fmt.Sprintf("%s/%s.txt", path, randomString())
		log.Println(filePath)

		// 将文本数据写入 MinIO
		_, err := minioClient.PutObject(
			c,
			BucketName,
			filePath,
			strings.NewReader(data.Content),
			int64(len(data.Content)),
			minio.PutObjectOptions{ContentType: "text/plain"},
		)
		if err != nil {
			log.Printf("failed to upload: %v", err)

			resp.Code = 2
			resp.Msg = "failed to upload"
			c.JSON(500, resp)
			return
		}

		// 返回文件 URL
		if AppURL == "" {
			AppURL = "http://localhost:8080"
		}

		url := fmt.Sprintf("%s/%s", AppURL, filePath)
		resp.Data = gin.H{"url": url}
		c.JSON(200, resp)
	})

	// 下载文件
	r.GET("/*any", func(c *gin.Context) {
		resp := &Resp{
			Code: 0,
			Msg:  "",
			Data: gin.H{},
		}

		filePath := strings.TrimLeft(c.Request.URL.Path, "/")
		if filePath == "" {
			resp.Code = 1
			resp.Msg = "Bad request"

			c.JSON(400, resp)
			return
		}

		// 从 MinIO 读取文件
		object, err := minioClient.GetObject(
			c,
			BucketName,
			filePath,
			minio.GetObjectOptions{},
		)
		if err != nil {
			log.Println("failed to get object: " + filePath)

			resp.Code = 2
			resp.Msg = "failed to get object"
			c.JSON(404, resp)
			return
		}

		// 返回文件
		fileInfo, err := object.Stat()
		if err != nil {
			log.Printf("failed to get object stat: %v", err)

			resp.Code = 3
			resp.Msg = "failed to get object stat"
			c.JSON(404, resp)
			return
		}
		fileSize := fileInfo.Size
		fileName := fileInfo.Key
		fileType := fileInfo.ContentType

		c.DataFromReader(200, fileSize, fileType, object, map[string]string{
			"Content-Disposition": fmt.Sprintf(`attachment; filename="%s"`, fileName),
		})
	})

	if AppPort == "" {
		AppPort = "8080"
	}
	r.Run(fmt.Sprintf(":%s", AppPort)) // listen and serve on 0.0.0.0:8080
}

// generate uuid
func randomString() string {
	return uuid.New().String()
}

func initMinIOClient() (*minio.Client, error) {
	// 初始化 MinIO 客户端
	c, err := minio.New(MinIOEndpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(AccessKey, SecretKey, ""),
		Secure: UseSSL,
	})

	return c, err
}

// initMinIOClient initializes a MinIO client by creating a new client with given
// credentials and endpoint options. It then checks if a bucket exists, creates
// the bucket if it doesn't exist, and sets its access policy to public.
func initMinIOBucket(ctx context.Context, c *minio.Client) {
	// 检查 bucket 是否存在
	exists, errBucketExists := c.BucketExists(ctx, BucketName)
	if errBucketExists != nil {
		log.Panicln("检查存储桶是否存在失败")
		panic(errBucketExists)
	}

	// 如果不存在则创建
	if !exists {
		log.Println("创建存储桶: " + BucketName)
		err := c.MakeBucket(ctx, BucketName, minio.MakeBucketOptions{})
		if err != nil {
			log.Panicln("创建存储桶失败")
			panic(err)
		}
	}
}

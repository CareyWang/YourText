package main

import (
	"fmt"
	"log"
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

var APP_URL string
var APP_PORT string

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

	// 从环境变量中获取 APP_URL
	APP_URL = os.Getenv("YOURTEXT_APP_URL")
	APP_PORT = os.Getenv("YOURTEXT_APP_PORT")

	// 从环境变量中获取 MinIO 配置
	MinIOEndpoint = os.Getenv("YOURTEXT_MINIO_ENDPOINT")
	AccessKey = os.Getenv("YOURTEXT_MINIO_ACCESS_KEY")
	SecretKey = os.Getenv("YOURTEXT_MINIO_SECRET_KEY")
	BucketName = os.Getenv("YOURTEXT_MINIO_BUCKET_NAME")
	UseSSL = false
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
		log.Printf("ballast lenL %v", len(ballast))
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 检查 bucket
	initMinIOBucket(ctx, minioClient)

	r := gin.Default()

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
			log.Fatalf("failed to upload text: %v", err)

			resp.Code = 2
			resp.Msg = "failed to upload text"
			c.JSON(500, resp)
			return
		}

		// 返回文件 URL
		if APP_URL == "" {
			APP_URL = "http://localhost:8080"
		}

		url := fmt.Sprintf("%s/%s", APP_URL, filePath)
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
			log.Fatalln("failed to get object: " + filePath)

			resp.Code = 2
			resp.Msg = "failed to get object"
			c.JSON(404, resp)
			return
		}

		// 返回文件
		fileInfo, err := object.Stat()
		if err != nil {
			log.Fatalf("failed to get object stat: %v", err)

			resp.Code = 3
			resp.Msg = "failed to get object stat"
			c.JSON(500, resp)
			return
		}
		fileSize := fileInfo.Size
		fileName := fileInfo.Key
		fileType := fileInfo.ContentType

		c.DataFromReader(200, fileSize, fileType, object, map[string]string{
			"Content-Disposition": fmt.Sprintf(`attachment; filename="%s"`, fileName),
		})
	})

	r.Run(fmt.Sprintf(":%s", APP_PORT)) // listen and serve on 0.0.0.0:8080
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
		log.Fatalln("检查存储桶是否存在失败")
		panic(errBucketExists)
	}

	// 如果不存在则创建
	if !exists {
		log.Println("创建存储桶: " + BucketName)
		err := c.MakeBucket(ctx, BucketName, minio.MakeBucketOptions{})
		if err != nil {
			log.Fatalln("创建存储桶失败")
			panic(err)
		}
	}
}

package main

// sync-minio 子命令：把存量数据从 MinIO 同步到新的 S3 兼容存储（如 Garage）。
//
// 特性：
//   - 只读源、只增不删目标：不会删除或修改源数据，也不会删除目标中已有的对象
//   - 幂等、可重复运行：目标已存在且大小一致的对象会被跳过，中断后重跑即可续传
//   - 同步结束后校验：确认源中的每个对象在目标中都存在且大小一致
//
// 凭证通过环境变量传入（避免出现在命令行 / 进程列表中）：
//
//	SRC_ACCESS_KEY  SRC_SECRET_KEY  DST_ACCESS_KEY  DST_SECRET_KEY
//
// 用法：
//
//	./YourText sync-minio \
//	    --src-endpoint localhost:9000 --src-bucket yourtext \
//	    --dst-endpoint localhost:3900 --dst-bucket yourtext --dry-run

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func newSyncClient(endpoint, region, accessKey, secretKey string) *s3.Client {
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	return s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		UsePathStyle: true,
		// 仅在必要时计算/校验校验和，兼容非 AWS 的 S3 服务
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// listObjects 返回 key -> size。
func listObjects(ctx context.Context, c *s3.Client, bucket, prefix string) (map[string]int64, error) {
	result := map[string]int64{}
	p := s3.NewListObjectsV2Paginator(c, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			result[aws.ToString(obj.Key)] = aws.ToInt64(obj.Size)
		}
	}
	return result, nil
}

func copyObject(ctx context.Context, src, dst *s3.Client, srcBucket, dstBucket, key string) (int64, error) {
	obj, err := src.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(srcBucket), Key: aws.String(key)})
	if err != nil {
		return 0, err
	}
	defer obj.Body.Close()

	body, err := io.ReadAll(obj.Body)
	if err != nil {
		return 0, err
	}
	if want := aws.ToInt64(obj.ContentLength); int64(len(body)) != want {
		return 0, fmt.Errorf("读取不完整: 期望 %d 字节，实际 %d 字节", want, len(body))
	}

	contentType := aws.ToString(obj.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err = dst.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(dstBucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	return int64(len(body)), err
}

// runSyncMinio 执行 sync-minio 子命令，返回进程退出码。
func runSyncMinio(args []string) int {
	fs := flag.NewFlagSet("sync-minio", flag.ContinueOnError)
	srcEndpoint := fs.String("src-endpoint", "", "源地址，如 localhost:9000 或 https://minio.example.com")
	srcBucket := fs.String("src-bucket", "", "源存储桶")
	srcRegion := fs.String("src-region", "us-east-1", "源 region")
	dstEndpoint := fs.String("dst-endpoint", "", "目标地址，如 localhost:3900")
	dstBucket := fs.String("dst-bucket", "", "目标存储桶，需已存在")
	dstRegion := fs.String("dst-region", "us-east-1", "目标 region")
	prefix := fs.String("prefix", "", "只同步该前缀下的对象，默认全部")
	workers := fs.Int("workers", 16, "并发数")
	dryRun := fs.Bool("dry-run", false, "只列出需要同步的对象，不写入")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *srcEndpoint == "" || *srcBucket == "" || *dstEndpoint == "" || *dstBucket == "" {
		fmt.Fprintln(os.Stderr, "必须指定 --src-endpoint --src-bucket --dst-endpoint --dst-bucket")
		fs.Usage()
		return 2
	}

	var creds [4]string
	for i, name := range []string{"SRC_ACCESS_KEY", "SRC_SECRET_KEY", "DST_ACCESS_KEY", "DST_SECRET_KEY"} {
		if creds[i] = os.Getenv(name); creds[i] == "" {
			fmt.Fprintf(os.Stderr, "缺少环境变量 %s\n", name)
			return 1
		}
	}

	ctx := context.Background()
	src := newSyncClient(*srcEndpoint, *srcRegion, creds[0], creds[1])
	dst := newSyncClient(*dstEndpoint, *dstRegion, creds[2], creds[3])

	for _, b := range []struct {
		name   string
		client *s3.Client
		bucket string
	}{{"源", src, *srcBucket}, {"目标", dst, *dstBucket}} {
		if _, err := b.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(b.bucket)}); err != nil {
			fmt.Fprintf(os.Stderr, "无法访问%s存储桶 %s: %v\n", b.name, b.bucket, err)
			return 1
		}
	}

	start := time.Now()
	srcObjs, err := listObjects(ctx, src, *srcBucket, *prefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "列出源对象失败: %v\n", err)
		return 1
	}
	dstObjs, err := listObjects(ctx, dst, *dstBucket, *prefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "列出目标对象失败: %v\n", err)
		return 1
	}

	var todo []string
	for k, size := range srcObjs {
		if s, ok := dstObjs[k]; !ok || s != size {
			todo = append(todo, k)
		}
	}
	fmt.Printf("源对象 %d 个，目标已有且一致 %d 个，待同步 %d 个\n", len(srcObjs), len(srcObjs)-len(todo), len(todo))

	if *dryRun {
		for i, k := range todo {
			if i == 20 {
				fmt.Printf("  ... 另有 %d 个\n", len(todo)-20)
				break
			}
			fmt.Printf("  [dry-run] %s (%d B)\n", k, srcObjs[k])
		}
		return 0
	}

	var copied, copiedBytes, failed, done atomic.Int64
	keys := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range keys {
				n, err := copyObject(ctx, src, dst, *srcBucket, *dstBucket, k)
				if err != nil {
					failed.Add(1)
					fmt.Fprintf(os.Stderr, "  失败: %s: %v\n", k, err)
				} else {
					copied.Add(1)
					copiedBytes.Add(n)
				}
				if d := done.Add(1); d%500 == 0 {
					fmt.Printf("  进度 %d/%d\n", d, len(todo))
				}
			}
		}()
	}
	for _, k := range todo {
		keys <- k
	}
	close(keys)
	wg.Wait()

	// 校验：源中每个对象在目标中都存在且大小一致
	final, err := listObjects(ctx, dst, *dstBucket, *prefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "校验时列出目标对象失败: %v\n", err)
		return 1
	}
	var bad []string
	for k, size := range srcObjs {
		if s, ok := final[k]; !ok || s != size {
			bad = append(bad, k)
		}
	}

	fmt.Printf("完成: 复制 %d 个 (%d B)，失败 %d 个，耗时 %.1fs\n", copied.Load(), copiedBytes.Load(), failed.Load(), time.Since(start).Seconds())
	if len(bad) > 0 {
		if len(bad) > 3 {
			bad = bad[:3]
		}
		fmt.Fprintf(os.Stderr, "校验未通过: 有对象在目标中缺失或大小不一致，例如 %v\n", bad)
		return 1
	}
	fmt.Printf("校验通过: 源 %d 个对象均已存在于目标且大小一致\n", len(srcObjs))
	return 0
}

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Config 是 S3 后端的配置。
type S3Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
	// PathStyle 为 true 时用 path 风格访问（RustFS/MinIO 必须）
	PathStyle bool
}

// S3 是基于 S3 协议的对象存储后端。
//
// 兼容：AWS S3 / RustFS / MinIO / 阿里云 OSS(S3 兼容模式) / 腾讯云 COS(S3 兼容)
//
// 与 LocalFS 的差异：
//   - 无目录概念，key 就是一整串
//   - 需要 bucket 预先存在（Init 时自动创建）
//   - 支持预签名 URL（直传/直下，减轻服务端压力）
type S3 struct {
	client *s3.Client
	bucket string
}

// NewS3 创建 S3 后端并确保 bucket 存在。
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("缺少 S3 endpoint")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("缺少 S3 bucket")
	}

	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("加载 S3 配置失败: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		// RustFS/MinIO 不支持 virtual-host 风格（需要 DNS 泛解析）
		o.UsePathStyle = cfg.PathStyle
		// 校验和只在需要时计算：部分 S3 兼容实现不支持新校验和头
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})

	s := &S3{client: client, bucket: cfg.Bucket}

	// 确保 bucket 存在
	if err := s.ensureBucket(ctx); err != nil {
		return nil, err
	}

	return s, nil
}

// ensureBucket 创建 bucket（已存在则忽略）。
func (s *S3) ensureBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err == nil {
		return nil // 已存在
	}

	// 不存在则创建
	_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		// 并发创建时可能报 BucketAlreadyOwnedByYou，视为成功
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			code := apiErr.ErrorCode()
			if code == "BucketAlreadyOwnedByYou" || code == "BucketAlreadyExists" {
				return nil
			}
		}
		return fmt.Errorf("创建 bucket %q 失败: %w", s.bucket, err)
	}
	return nil
}

// Put 写入对象。
func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, mime string) (int64, error) {
	if err := ValidateKey(key); err != nil {
		return 0, err
	}

	// S3 需要知道长度；未知时先读到内存（我们的场景文件都不大）
	var body io.Reader = r
	contentLength := size
	if size < 0 {
		data, err := io.ReadAll(r)
		if err != nil {
			return 0, fmt.Errorf("读取内容失败: %w", err)
		}
		body = bytes.NewReader(data)
		contentLength = int64(len(data))
	}

	input := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(contentLength),
	}
	if mime != "" {
		input.ContentType = aws.String(mime)
	}
	// 私有桶：不设 ACL，由 bucket 策略统一控制

	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return 0, fmt.Errorf("上传到 S3 失败: %w", err)
	}
	return contentLength, nil
}

// Get 读取对象。
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("从 S3 读取失败: %w", err)
	}
	return out.Body, nil
}

// Stat 查询对象信息。
func (s *S3) Stat(ctx context.Context, key string) (*ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("查询 S3 对象失败: %w", err)
	}

	info := &ObjectInfo{Key: key}
	if out.ContentLength != nil {
		info.Size = *out.ContentLength
	}
	if out.LastModified != nil {
		info.Modified = *out.LastModified
	}
	if out.ETag != nil {
		info.Hash = strings.Trim(*out.ETag, `"`)
	}
	return info, nil
}

// Delete 删除对象。
func (s *S3) Delete(ctx context.Context, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("删除 S3 对象失败: %w", err)
	}
	return nil // 幂等
}

// Exists 检查对象是否存在。
func (s *S3) Exists(ctx context.Context, key string) (bool, error) {
	_, err := s.Stat(ctx, key)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return false, err
}

// PresignGet 生成预签名下载 URL。
//
// 用途：前端直接下载大文件，不经过 Go 服务，减轻带宽压力。
// 注意：预签名 URL 的 host 是内网地址，前端可能访问不到，
// 所以默认不启用，仍走服务端代理下载。
func (s *S3) PresignGet(ctx context.Context, key string, expirySeconds int64) (string, error) {
	presigner := s3.NewPresignClient(s.client)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(o *s3.PresignOptions) {
		o.Expires = durationSeconds(expirySeconds)
	})
	if err != nil {
		return "", fmt.Errorf("生成预签名 URL 失败: %w", err)
	}
	return req.URL, nil
}

// Health 检查 S3 后端可用性。
func (s *S3) Health(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	})
	if err != nil {
		return fmt.Errorf("S3 bucket 不可用: %w", err)
	}
	return nil
}

// isNotFound 判断是否为「对象不存在」。
func isNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		if code == "NoSuchKey" || code == "NotFound" || code == "404" {
			return true
		}
	}
	return false
}

// 编译期断言
var _ Backend = (*S3)(nil)

// durationSeconds 把秒数转成 time.Duration。
func durationSeconds(s int64) time.Duration {
	if s <= 0 {
		s = 3600
	}
	return time.Duration(s) * time.Second
}

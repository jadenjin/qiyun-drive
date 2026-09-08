package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"pan/backend/internal/config"
)

type Store struct {
	client        *s3.Client
	signer        *s3.PresignClient
	bucket        string
	allowedOrigin string
	ttl           time.Duration
}

type CompletedPart struct {
	Number int32  `json:"partNumber"`
	ETag   string `json:"etag"`
}

func New(cfg config.Config) *Store {
	endpoint := cfg.S3Endpoint
	client := s3.New(s3.Options{
		Region:       cfg.S3Region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		UsePathStyle: true,
	})
	publicClient := client
	if cfg.S3PublicEndpoint != "" && cfg.S3PublicEndpoint != cfg.S3Endpoint {
		publicClient = s3.New(s3.Options{
			Region:       cfg.S3Region,
			BaseEndpoint: aws.String(cfg.S3PublicEndpoint),
			Credentials:  credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
			UsePathStyle: true,
		})
	}
	return &Store{
		client: client, signer: s3.NewPresignClient(publicClient), bucket: cfg.S3Bucket,
		allowedOrigin: cfg.AllowedOrigin, ttl: cfg.PresignTTL,
	}
}

func (s *Store) EnsureBucket(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		if _, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
			return err
		}
	}
	_, err := s.client.PutBucketCors(ctx, &s3.PutBucketCorsInput{
		Bucket: aws.String(s.bucket),
		CORSConfiguration: &types.CORSConfiguration{CORSRules: []types.CORSRule{{
			AllowedHeaders: []string{"*"},
			AllowedMethods: []string{"GET", "PUT", "POST", "HEAD"},
			AllowedOrigins: []string{s.allowedOrigin},
			ExposeHeaders:  []string{"ETag", "x-amz-request-id"},
			MaxAgeSeconds:  aws.Int32(3600),
		}}},
	})
	return err
}

func (s *Store) Ready(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	return err
}

func (s *Store) PresignPut(ctx context.Context, key, mime string, size int64) (string, error) {
	request, err := s.signer.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String(mime), ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(s.ttl))
	if err != nil {
		return "", err
	}
	return request.URL, nil
}

func (s *Store) CreateMultipart(ctx context.Context, key, mime string) (string, error) {
	result, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ContentType: aws.String(mime),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(result.UploadId), nil
}

func (s *Store) PresignPart(ctx context.Context, key, uploadID string, part int32, size int64) (string, error) {
	request, err := s.signer.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(part), ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(s.ttl))
	if err != nil {
		return "", err
	}
	return request.URL, nil
}

func (s *Store) CompleteMultipart(ctx context.Context, key, uploadID string, parts []CompletedPart) error {
	completed := make([]types.CompletedPart, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, types.CompletedPart{PartNumber: aws.Int32(part.Number), ETag: aws.String(part.ETag)})
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	return err
}

func (s *Store) AbortMultipart(ctx context.Context, key, uploadID string) error {
	if uploadID == "" {
		return nil
	}
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchUpload" {
		return nil
	}
	return err
}

func (s *Store) Head(ctx context.Context, key string) (int64, string, error) {
	result, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return 0, "", err
	}
	return aws.ToInt64(result.ContentLength), aws.ToString(result.ContentType), nil
}

func (s *Store) PresignGet(ctx context.Context, key, filename string) (string, error) {
	disposition := "inline"
	if filename != "" {
		disposition = fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(filename))
	}
	request, err := s.signer.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), ResponseContentDisposition: aws.String(disposition),
	}, s3.WithPresignExpires(s.ttl))
	if err != nil {
		return "", err
	}
	return request.URL, nil
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}

func (s *Store) Put(ctx context.Context, key, mime string, body io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String(mime),
	})
	return err
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

// Package s3 implements Q v2's storage.BlobStore over S3-compatible object storage.
package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/snowmerak/q/v2/pkg/storage"
)

// Config configures the S3 BlobStore adapter.
type Config struct {
	Endpoint     string
	Bucket       string
	Region       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
	HTTPClient   *http.Client
}

// Store implements storage.BlobStore using AWS SDK v2 S3.
type Store struct {
	client *s3.Client
	bucket string
}

// Open validates config and constructs an S3-backed storage.BlobStore.
func Open(ctx context.Context, cfg Config) (*Store, error) {
	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" {
		return nil, fmt.Errorf("%w: s3 bucket is required", storage.ErrInvalidInput)
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, config.WithHTTPClient(cfg.HTTPClient))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if ep := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"); ep != "" {
			o.BaseEndpoint = aws.String(ep)
		}
		o.UsePathStyle = cfg.UsePathStyle
	})

	return &Store{client: client, bucket: bucket}, nil
}

func cleanKey(key string) (string, error) {
	if key == "" || strings.HasPrefix(key, "/") || strings.ContainsRune(key, '\\') || strings.Contains(key, "\x00") {
		return "", fmt.Errorf("%w: invalid blob key %q", storage.ErrInvalidInput, key)
	}
	parts := strings.Split(key, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", fmt.Errorf("%w: invalid path segment in blob key %q", storage.ErrInvalidInput, key)
		}
	}
	cleaned := path.Clean(key)
	if cleaned == "." || strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("%w: invalid blob key %q", storage.ErrInvalidInput, key)
	}
	return cleaned, nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader, mediaType string) (int64, string, error) {
	cleaned, err := cleanKey(key)
	if err != nil {
		return 0, "", err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, "", err
	}
	sum := sha256.Sum256(data)
	sumHex := hex.EncodeToString(sum[:])

	in := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(cleaned),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	}
	if mt := strings.TrimSpace(mediaType); mt != "" {
		in.ContentType = aws.String(mt)
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return 0, "", mapS3Error(err)
	}
	return int64(len(data)), sumHex, nil
}

func (s *Store) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	cleaned, err := cleanKey(key)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(cleaned),
	})
	if err != nil {
		return nil, mapS3Error(err)
	}
	return out.Body, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	cleaned, err := cleanKey(key)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(cleaned),
	})
	if err != nil {
		mapped := mapS3Error(err)
		if errors.Is(mapped, storage.ErrNotFound) {
			return nil
		}
		return mapped
	}
	return nil
}

func (s *Store) Close() error { return nil }

func mapS3Error(err error) error {
	if err == nil {
		return nil
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
		return storage.ErrNotFound
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return storage.ErrNotFound
		}
	}
	return err
}

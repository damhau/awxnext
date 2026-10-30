// Package blob stores job event archives in object storage: Amazon S3 or an
// S3-compatible service, and Azure Blob Storage. A stored object's location
// is "<bucket or container>/<key>".
package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Store keeps objects.
type Store interface {
	// Put stores data under key and returns its location.
	Put(ctx context.Context, key string, data []byte) (string, error)
	Get(ctx context.Context, location string) ([]byte, error)
	// Delete removes an object; one already gone is not an error.
	Delete(ctx context.Context, location string) error
}

func split(location string) (string, string, error) {
	bucket, key, ok := strings.Cut(location, "/")
	if !ok || bucket == "" || key == "" {
		return "", "", fmt.Errorf("invalid object location %q", location)
	}
	return bucket, key, nil
}

// S3Config is an S3 bucket. Without an access key, credentials come from
// the environment: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, the shared
// configuration files, a web identity (EKS) or the instance's role.
type S3Config struct {
	Bucket string
	// Endpoint is an S3-compatible service (MinIO, Ceph...), addressed by
	// path; empty for AWS.
	Endpoint                           string
	Region                             string
	AccessKey, SecretKey, SessionToken string
}

type s3Store struct {
	client *s3.Client
	bucket string
}

// NewS3 opens an S3 bucket.
func NewS3(ctx context.Context, c S3Config) (Store, error) {
	if c.Bucket == "" {
		return nil, errors.New("no bucket is set")
	}
	opts := []func(*config.LoadOptions) error{}
	if c.Region != "" {
		opts = append(opts, config.WithRegion(c.Region))
	}
	if c.AccessKey != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKey, c.SecretKey, c.SessionToken)))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
			o.UsePathStyle = true
			// S3-compatible services may not take the checksums AWS adds.
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
	})
	return &s3Store{client: client, bucket: c.Bucket}, nil
}

func (s *s3Store) Put(ctx context.Context, key string, data []byte) (string, error) {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key),
		Body: bytes.NewReader(data), ContentType: aws.String("application/gzip")})
	if err != nil {
		return "", err
	}
	return s.bucket + "/" + key, nil
}

func (s *s3Store) Get(ctx context.Context, location string) ([]byte, error) {
	bucket, key, err := split(location)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *s3Store) Delete(ctx context.Context, location string) error {
	bucket, key, err := split(location)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	var missing *types.NoSuchKey
	if errors.As(err, &missing) {
		return nil
	}
	return err
}

// AzureConfig is an Azure Blob Storage container. URL is the account's
// blob endpoint (https://<account>.blob.core.windows.net/). With a client
// ID and secret, a service principal signs in; without, the environment:
// AZURE_STORAGE_CONNECTION_STRING (an account key, as for Azurite), else
// azidentity's default chain (environment, workload identity, managed
// identity).
type AzureConfig struct {
	Container, URL         string
	Tenant, Client, Secret string
}

type azureStore struct {
	client    *azblob.Client
	container string
}

// NewAzure opens an Azure Blob Storage container.
func NewAzure(ctx context.Context, c AzureConfig) (Store, error) {
	if c.Container == "" {
		return nil, errors.New("no container is set")
	}
	var client *azblob.Client
	var err error
	switch {
	case c.Client != "":
		var cred azcore.TokenCredential
		if cred, err = azidentity.NewClientSecretCredential(c.Tenant, c.Client, c.Secret, nil); err == nil {
			client, err = azblob.NewClient(c.URL, cred, nil)
		}
	case os.Getenv("AZURE_STORAGE_CONNECTION_STRING") != "":
		client, err = azblob.NewClientFromConnectionString(os.Getenv("AZURE_STORAGE_CONNECTION_STRING"), nil)
	default:
		if c.URL == "" {
			return nil, errors.New("no blob endpoint is set")
		}
		var cred azcore.TokenCredential
		if cred, err = azidentity.NewDefaultAzureCredential(nil); err == nil {
			client, err = azblob.NewClient(c.URL, cred, nil)
		}
	}
	if err != nil {
		return nil, err
	}
	return &azureStore{client: client, container: c.Container}, nil
}

func (s *azureStore) Put(ctx context.Context, key string, data []byte) (string, error) {
	if _, err := s.client.UploadBuffer(ctx, s.container, key, data, nil); err != nil {
		return "", err
	}
	return s.container + "/" + key, nil
}

func (s *azureStore) Get(ctx context.Context, location string) ([]byte, error) {
	container, key, err := split(location)
	if err != nil {
		return nil, err
	}
	out, err := s.client.DownloadStream(ctx, container, key, nil)
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *azureStore) Delete(ctx context.Context, location string) error {
	container, key, err := split(location)
	if err != nil {
		return err
	}
	_, err = s.client.DeleteBlob(ctx, container, key, nil)
	if bloberror.HasCode(err, bloberror.BlobNotFound) {
		return nil
	}
	return err
}

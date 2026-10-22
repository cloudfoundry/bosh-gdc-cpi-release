// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/s3"
	objectv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/object/v1"
)

const (
	s3TimeoutInSeconds               = 100
	gdcCustomErrCodeNoSuchKeyPattern = `object .* does not exist`
)

type Storage struct {
	client      client.Client
	logger      cfboshlog.Logger
	projectID   string
	credentials *credentials
}

type UploadOptions struct {
	// name of the object after being uploaded to object storage
	ObjectName string
	// local path of the file to be uploaded
	FilePath string
	// object storage bucket name
	BucketName string
}

type checkObjectExistenceOptions struct {
	// name of the object after being uploaded to object storage
	objectName string
	// GDCH storage client
	storageClient *storageClient
}

type storageClient struct {
	// http client used to call GDCH object storage's S3 compatible rest APIs
	S3Client s3.Client
	// object storage bucket object
	Bucket *objectv1.Bucket
}

type credentials struct {
	accessKeyId     string
	secretAccessKey string
	certs           *[]byte
}

func NewGDCStorage(
	cloudClient client.Client,
	logger cfboshlog.Logger, projectID string,
	serviceAccount string,
	encodedTLSCerts string,
	s3Credential *config.S3Credential,
) (*Storage, error) {
	storage := &Storage{
		client:    cloudClient,
		logger:    logger,
		projectID: projectID,
	}

	tlsCertificate, err := base64.StdEncoding.DecodeString(encodedTLSCerts)
	if err != nil {
		return nil, fmt.Errorf("decode TLS certificates: %w", err)
	}
	accessKeyID, err := base64.StdEncoding.DecodeString(s3Credential.AccessKeyID)
	if err != nil {
		return nil, fmt.Errorf("decode access key ID: %w", err)
	}
	accessKey, err := base64.StdEncoding.DecodeString(s3Credential.AccessKey)
	if err != nil {
		return nil, fmt.Errorf("decode secret access key: %w", err)
	}

	storage.credentials = &credentials{
		accessKeyId:     string(accessKeyID),
		secretAccessKey: string(accessKey),
		certs:           &tlsCertificate,
	}

	return storage, nil
}

// Upload local file as object to specified GDCH object storage bucket.
func (s *Storage) UploadFromLocalPath(opts *UploadOptions) error {
	client, err := s.getS3ClientOnBucket(opts.BucketName)
	if err != nil {
		return fmt.Errorf("unable to set up S3 client: %w", err)
	}

	file, err := os.Open(opts.FilePath)
	if err != nil {
		return fmt.Errorf("open source file at %q: %w", opts.FilePath, err)
	}
	defer file.Close()

	objectExists, err := s.doesObjectExistInBucket(
		&checkObjectExistenceOptions{
			objectName:    opts.ObjectName,
			storageClient: client,
		},
	)
	if err != nil {
		return err
	}
	if objectExists {
		s.logger.Info("UploadFromLocalPath", "object %q already exists in bucket %q, skip re-uploading", opts.ObjectName, opts.BucketName)
		return nil
	}

	input := s3.UploadObjectInput{
		BucketFqn: client.Bucket.Status.FullyQualifiedName,
		Key:       opts.ObjectName,
		Reader:    file,
	}

	if _, err := client.S3Client.UploadObject(input); err != nil {
		return fmt.Errorf("unable to upload source file at %q to bucket: %w", opts.FilePath, err)
	}
	return nil
}

// Check if object already exists in bucket
func (s *Storage) doesObjectExistInBucket(opts *checkObjectExistenceOptions) (bool, error) {
	storageClient := opts.storageClient
	input := s3.GetObjectInput{
		BucketFqn: storageClient.Bucket.Status.FullyQualifiedName,
		ObjectKey: opts.objectName,
	}
	if _, err := storageClient.S3Client.GetObject(input); err != nil {
		re := regexp.MustCompile(gdcCustomErrCodeNoSuchKeyPattern)
		if re.MatchString(err.Error()) {
			return false, nil
		}

		// In GDCH releases 1.14.3+, the obj storage team
		// overrides AWS error codes, so the below `if` block
		// works only on releases prior to 1.14.3.
		// The previous `if` block is enough for GDCH versions 1.14.3+
		if aerr, ok := err.(awserr.Error); ok {
			if aerr.Code() == awss3.ErrCodeNoSuchKey {
				return false, nil
			}
		}

		return true, fmt.Errorf("unable to check object existence: %w", err)
	}

	return true, nil
}

// Get S3 client for certain object storage bucket.
func (s *Storage) getS3ClientOnBucket(bucketName string) (*storageClient, error) {
	ctx := context.Background()
	bucket := &objectv1.Bucket{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: bucketName, Namespace: s.projectID}, bucket); err != nil {
		return nil, fmt.Errorf("get object storage bucket %q: %w", bucketName, err)
	}

	s3Config := s3.Config{
		AccessKeyId:     s.credentials.accessKeyId,
		SecretAccessKey: s.credentials.secretAccessKey,
		S3Certificate:   *s.credentials.certs,
		EndpointUrl:     bucket.Status.Endpoint,
		Region:          bucket.Status.Region,
	}

	tr := &http.Transport{}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(s3Config.S3Certificate)
	tr.TLSClientConfig = &tls.Config{RootCAs: caCertPool}
	s3Config.CustomHttpClient = &http.Client{
		Transport: tr,
		Timeout:   s3TimeoutInSeconds * time.Second,
	}

	s3Client, err := s3.NewGDCS3Client(&s3Config)
	if err != nil {
		return nil, fmt.Errorf("initialize S3 client for bucket %q: %w",
			bucketName, err)
	}

	return &storageClient{
		S3Client: s3Client,
		Bucket:   bucket,
	}, nil
}

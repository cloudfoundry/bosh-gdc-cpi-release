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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/s3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	objectv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/object/v1"
)

const (
	defaultProject           = "test-project"
	defaultServiceAccount    = "SAP-on-GDCH"
	defaultEncodedSecretName = "object-storage-key-std-sa-72a6oeh4zbhlh2ex2wmhnc7a6julshz52hafb2r3d3hpdu4orwpa"
	defaultBucketName        = "bucket"
	defaultBucketFQN         = "bucket-fqn"
	defaultImageName         = "image"
	defaultS3Endpoint        = "gdch/s3"
	defaultRegion            = "org-1"
	defaultDir               = "ut_path"
	defaultEncodedTLSCerts   = "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tIFNPTUVfQ0VSVFMgLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo="
	defaultTLSCerts          = "-----BEGIN CERTIFICATE----- SOME_CERTS -----END CERTIFICATE-----\n"
)

var (
	logger            = cfboshlog.New(boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr))
	defaultSecret     = createSecret(defaultProject, defaultEncodedSecretName)
	defaultBucket     = createBucket(defaultBucketName, defaultS3Endpoint, defaultRegion, objectv1.BucketReady)
	defaultByoBucket  = createBucket("vm-images-bucket", defaultS3Endpoint, defaultRegion, objectv1.BucketReady)
	defaultUploadOpts = &UploadOptions{
		ObjectName: defaultImageName,
		FilePath:   fmt.Sprintf("%s/%s", defaultDir, defaultImageName),
		BucketName: defaultBucketName,
	}
	defaultCerts         = []byte("certs")
	defaultS3Credentials = &config.S3Credential{
		AccessKeyID: "YWNjZXNzX2lkCg==", // base64 encoding of "access_id"
		AccessKey:   "YWNjZXNzX2tleQo=", // base64 encoding of "access_key"
	}
	defaultCredentials = &credentials{
		accessKeyId:     "access_id",
		secretAccessKey: "access_key",
		certs:           &defaultCerts,
	}
)

func TestNewStorage_Succeed(t *testing.T) {
	c := test.FakeClient(defaultSecret, defaultByoBucket)
	storage, err := NewGDCStorage(c, logger, defaultProject, defaultServiceAccount, defaultEncodedTLSCerts, defaultS3Credentials)
	if err != nil {
		t.Fatalf("got expected error: %v", err)
	}

	gotCerts := string(*storage.credentials.certs)
	if gotCerts != defaultTLSCerts {
		t.Fatalf("got certs: %q, want: %q", gotCerts, defaultTLSCerts)
	}
}

func TestNewStorage_Fail_Setup(t *testing.T) {
	testCases := map[string]struct {
		// secret        *corev1.Secret
		byoBycket     *objectv1.Bucket
		encodedCerts  string
		s3Credentials *config.S3Credential
		expectedErr   string
	}{
		"failure on decoding TLS certs": {
			encodedCerts: "XXXXXaGVsbG8=",
			expectedErr:  "decode TLS certificates: illegal base64 data",
		},
		"failure on decoding access key ID": {
			encodedCerts: defaultEncodedTLSCerts,
			s3Credentials: &config.S3Credential{
				AccessKeyID: "XXXXXaGVsbG8=",
				AccessKey:   "YWNjZXNzX2tleQo=",
			},
			expectedErr: "decode access key ID",
		},
		"failure on decoding access key": {
			encodedCerts: defaultEncodedTLSCerts,
			s3Credentials: &config.S3Credential{
				AccessKeyID: "YWNjZXNzX2lkCg==",
				AccessKey:   "XXXXXaGVsbG8=",
			},
			expectedErr: "decode secret access key",
		},
	}

	c := test.FakeClient()
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			_, err := NewGDCStorage(c, logger, defaultProject, defaultServiceAccount, tc.encodedCerts, tc.s3Credentials)
			if err == nil {
				t.Fatal("got nil, expected error")
			}

			gotError := err.Error()
			if !strings.Contains(gotError, tc.expectedErr) {
				t.Fatalf("got error '%s', expected error '%s'", gotError, tc.expectedErr)
			}
		})
	}
}

func TestGetS3ClientOnBucket_Fail_GetBucket(t *testing.T) {
	expectedErr := "get object storage bucket \"bucket\""
	c := test.FakeInvalidClient()
	storage := &Storage{
		client:      c,
		projectID:   defaultProject,
		credentials: defaultCredentials,
	}

	_, err := storage.getS3ClientOnBucket(defaultBucketName)
	if err == nil {
		t.Fatalf("got nil, expected error %v", err)
	}

	gotError := err.Error()
	if !strings.Contains(gotError, expectedErr) {
		t.Fatalf("got error '%s', expected error '%s'", gotError, expectedErr)
	}
}

func TestGetS3ClientOnBucket_Succeed(t *testing.T) {
	bucket := createBucket(defaultBucketName, "url", "region", objectv1.BucketReady)
	c := test.FakeClient(bucket)
	storage := &Storage{
		client:      c,
		projectID:   defaultProject,
		credentials: defaultCredentials,
	}

	_, err := storage.getS3ClientOnBucket(defaultBucketName)
	if err != nil {
		t.Fatalf("got unexpected error %v", err)
	}
}

func TestUploadFromLocalPath_Fail_S3ClientSetup(t *testing.T) {
	testCases := map[string]struct {
		bucket      *objectv1.Bucket
		expectedErr string
	}{
		"missing storage endpoint": {
			bucket:      createBucket(defaultBucketName, "", "region", objectv1.BucketReady),
			expectedErr: "MissingEndpointURL",
		},
		"missing region": {
			bucket:      createBucket(defaultBucketName, "url", "", objectv1.BucketReady),
			expectedErr: "MissingRegion",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			c := test.FakeClient(defaultSecret, defaultByoBucket, tc.bucket)
			storage, err := NewGDCStorage(c, logger, defaultProject, defaultServiceAccount, defaultEncodedTLSCerts, defaultS3Credentials)
			if err != nil {
				t.Fatalf("got expected error: %v", err)
			}

			err = storage.UploadFromLocalPath(defaultUploadOpts)
			if err == nil {
				t.Fatalf("got nil, expected error %v", err)
			}

			gotError := err.Error()
			if !strings.Contains(gotError, tc.expectedErr) {
				t.Fatalf("got error '%s', expected error '%s'", gotError, tc.expectedErr)
			}
		})
	}
}

func TestUploadFromLocalPath_Fail_UploadOptions(t *testing.T) {
	defaultImageFilePath := filepath.Join(t.TempDir(), defaultImageName)
	if err := os.WriteFile(defaultImageFilePath, []byte("some content"), 0o644); err != nil {
		t.Fatalf("unexpected error on writing %s: %v", defaultImageFilePath, err)
	}

	testCases := map[string]struct {
		imagePath   string
		bucket      *objectv1.Bucket
		expectedErr string
	}{
		"no source file": {
			imagePath:   "path/image",
			bucket:      defaultBucket,
			expectedErr: "no such file or director",
		},
		"no bucket FQN": {
			imagePath: defaultImageFilePath,
			bucket: &objectv1.Bucket{
				ObjectMeta: metav1.ObjectMeta{
					Name:      defaultBucketName,
					Namespace: defaultProject,
				},
				Status: objectv1.BucketStatus{
					Endpoint: defaultS3Endpoint,
					Region:   defaultRegion,
					Conditions: []metav1.Condition{
						{
							Type:               objectv1.BucketReady,
							Reason:             "worked",
							LastTransitionTime: metav1.Now(),
							Status:             metav1.ConditionTrue,
						},
					},
				},
			},
			expectedErr: "InvalidParameter: 1 validation error(s) found",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			c := test.FakeClient(defaultSecret, defaultByoBucket, tc.bucket)
			storage, err := NewGDCStorage(c, logger, defaultProject, defaultServiceAccount, defaultEncodedTLSCerts, defaultS3Credentials)
			if err != nil {
				t.Fatalf("got expected error: %v", err)
			}

			uploadOpts := &UploadOptions{
				ObjectName: defaultImageName,
				FilePath:   tc.imagePath,
				BucketName: defaultBucketName,
			}
			err = storage.UploadFromLocalPath(uploadOpts)
			if err == nil {
				t.Fatalf("got nil, expected error %v", err)
			}

			gotError := err.Error()
			if !strings.Contains(gotError, tc.expectedErr) {
				t.Fatalf("got error '%s', expected error '%s'", gotError, tc.expectedErr)
			}
		})
	}
}

func TestCheckObjectExistsInBucket_Succeed(t *testing.T) {
	c := test.FakeClient(defaultSecret, defaultByoBucket)
	storage, err := NewGDCStorage(c, logger, defaultProject, defaultServiceAccount, defaultEncodedTLSCerts, defaultS3Credentials)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	lastModified := time.Now()
	testCases := map[string]struct {
		expectedExistence bool
		buckets           map[string]*s3.MockBucket
	}{
		"object exists in bucket": {
			expectedExistence: true,
			buckets: map[string]*s3.MockBucket{
				defaultBucketFQN: {
					Objects: map[string]*s3.MockObject{
						defaultImageName: {
							Versions: []*s3.MockObjectVersion{
								{
									Data:         []byte("data"),
									IsLatest:     true,
									LastModified: &lastModified,
								},
							},
						},
					},
				},
			},
		},
		"object does not exists in bucket": {
			expectedExistence: false,
			buckets: map[string]*s3.MockBucket{
				defaultBucketFQN: {},
			},
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			storageClient := &storageClient{
				S3Client: s3.CreateMockS3Client(
					s3.MockS3ClientConfig{
						Buckets: tc.buckets,
					},
				),
				Bucket: defaultBucket,
			}

			exists, err := storage.doesObjectExistInBucket(
				&checkObjectExistenceOptions{
					objectName:    defaultImageName,
					storageClient: storageClient,
				},
			)
			if err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}

			expectedExistence := tc.expectedExistence
			if exists != expectedExistence {
				t.Fatalf("check object existence, got %t, expected %t", exists, expectedExistence)
			}
		})
	}
}

func TestCheckObjectExistsInBucket_Fail_InvalidObjectName(t *testing.T) {
	c := test.FakeClient(defaultSecret, defaultByoBucket)
	storage, err := NewGDCStorage(c, logger, defaultProject, defaultServiceAccount, defaultEncodedTLSCerts, defaultS3Credentials)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	storageClient := &storageClient{
		S3Client: s3.CreateMockS3Client(
			s3.MockS3ClientConfig{
				Buckets: map[string]*s3.MockBucket{
					defaultBucketFQN: {},
				},
			},
		),
		Bucket: defaultBucket,
	}

	_, err = storage.doesObjectExistInBucket(
		&checkObjectExistenceOptions{
			objectName:    "",
			storageClient: storageClient,
		},
	)
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	expectedErr := "object key must be non-empty"
	gotError := err.Error()
	if !strings.Contains(gotError, expectedErr) {
		t.Fatalf("got error %v', expected error %v", gotError, expectedErr)
	}
}

// test helpers
func createSecret(namespace string, secretName string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"access-key-id":     []byte("default_access_id"),
			"secret-access-key": []byte("default_access_key"),
		},
	}
}

func createBucket(bucketName string, endpoint string, region string, statusType string) *objectv1.Bucket {
	return &objectv1.Bucket{
		ObjectMeta: metav1.ObjectMeta{
			Name:      bucketName,
			Namespace: defaultProject,
		},
		Status: objectv1.BucketStatus{
			FullyQualifiedName: defaultBucketFQN,
			Endpoint:           endpoint,
			Region:             region,
			Conditions: []metav1.Condition{
				{
					Type:               statusType,
					Reason:             "worked",
					LastTransitionTime: metav1.Now(),
					Status:             metav1.ConditionTrue,
				},
			},
		},
	}
}

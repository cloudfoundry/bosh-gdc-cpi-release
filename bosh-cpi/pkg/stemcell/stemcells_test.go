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

package stemcell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vmconsts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/image"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/storage"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

var (
	defaultImagePath  = "/root/path/image.file"
	defaultName       = "gdch-stemcell"
	defaultVersion    = "0.0.0"
	defaultInfra      = "gdch"
	defaultHypervisor = "kvm"
	randomVersion     = "1.2.3"
	staticUUID        = "aa937100-7c2f-11ee-b962-0242ac120002"
	shortUUID         = strings.Split(staticUUID, "-")[0]
	defaultStemcellID = apiv1.NewStemcellCID(
		fmt.Sprintf("stemcell-%s-%s", defaultVersion, shortUUID),
	)
	randomStemcellID = apiv1.NewStemcellCID(
		fmt.Sprintf("stemcell-%s-%s", randomVersion, shortUUID),
	)
	defaultStemcellCloudProps, _ = convertToStemcellCloudProps(
		Property{
			Name:           defaultName,
			Version:        defaultVersion,
			Infrastructure: defaultInfra,
			Hypervisor:     defaultHypervisor,
		},
	)
	defaultVMImage = &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultStemcellID.AsString(),
			Namespace: projectID,
		},
	}
	defaultStorage  = &SuccessStorage{}
	defaultProperty = Property{
		Name:           defaultName,
		Version:        defaultVersion,
		Infrastructure: defaultInfra,
		Hypervisor:     defaultHypervisor,
	}

	projectID = "test-project"
	logger    = cfboshlog.New(boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr))
	uuidGen   = &staticUUIDGenerator{}
)

func TestCreateStemcell_Fail_EmptyImagePath(t *testing.T) {
	c := test.FakeClient()
	ic := &image.Client{
		Client:    c,
		ProjectID: projectID,
	}
	stemcells := NewStemcells(c, projectID, defaultStorage, ic, uuidGen, logger, &test.Timeout, false)

	_, err := stemcells.CreateStemcell("", defaultStemcellCloudProps)
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	gotError := err.Error()
	expectedError := "must provide non-empty imagePath"
	if !strings.Contains(gotError, expectedError) {
		t.Fatalf("got error '%s', expected error '%s'", gotError, expectedError)
	}
}

func TestCreateStemcell_Fail_PropertyParse(t *testing.T) {
	badJson := `{"name":"bad_name...}`
	stemcellCloudProps := &StemcellCloudPropsImpl{
		RawMessage: json.RawMessage(badJson),
	}

	c := test.FakeClient()
	ic := &image.Client{
		Client:    c,
		ProjectID: projectID,
	}
	stemcells := NewStemcells(c, projectID, defaultStorage, ic, uuidGen, logger, &test.Timeout, false)

	_, err := stemcells.CreateStemcell(defaultImagePath, stemcellCloudProps)
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	gotError := err.Error()
	expectedError := "parse stemcell properties"
	if !strings.Contains(gotError, expectedError) {
		t.Fatalf("got error '%s', expected error '%s'", gotError, expectedError)
	}
}

func TestCreateStemcell_Fail_PropertyValidation(t *testing.T) {
	c := test.FakeClient()
	ic := &image.Client{
		Client:    c,
		ProjectID: projectID,
	}
	stemcells := NewStemcells(c, projectID, defaultStorage, ic, uuidGen, logger, &test.Timeout, false)

	testCases := map[string]struct {
		props       Property
		expectedErr string
	}{
		"Name not provided": {
			props: Property{
				Version:        defaultVersion,
				Infrastructure: defaultInfra,
				Hypervisor:     defaultHypervisor,
			},
			expectedErr: "must provide non-empty Name",
		},
		"Name empty": {
			props: Property{
				Name:           "",
				Version:        defaultVersion,
				Infrastructure: defaultInfra,
				Hypervisor:     defaultHypervisor,
			},
			expectedErr: "must provide non-empty Name",
		},
		"Version not provided": {
			props: Property{
				Name:           defaultName,
				Infrastructure: defaultInfra,
				Hypervisor:     defaultHypervisor,
			},
			expectedErr: "must provide non-empty Version",
		},
		"Version empty": {
			props: Property{
				Name:           defaultName,
				Version:        "",
				Infrastructure: defaultInfra,
				Hypervisor:     defaultHypervisor,
			},
			expectedErr: "must provide non-empty Version",
		},
		"Unsupported infrastructure": {
			props: Property{
				Name:           defaultName,
				Version:        defaultVersion,
				Infrastructure: "unknown_infra",
				Hypervisor:     defaultHypervisor,
			},
			expectedErr: "Infrastruction must be 'gdch'",
		},
		"Unsupported hypervisor": {
			props: Property{
				Name:           defaultName,
				Version:        defaultVersion,
				Infrastructure: defaultInfra,
				Hypervisor:     "unknown_hypervisor",
			},
			expectedErr: "Hypervisor must be 'kvm'",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			stemcellCloudProps, err := convertToStemcellCloudProps(tc.props)
			if err != nil {
				t.Fatalf("convert to stemcellCloudProps from %v", tc.props)
			}

			_, err = stemcells.CreateStemcell(defaultImagePath, stemcellCloudProps)
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

func TestCreateStemcell_Fail_UUIDGeneration(t *testing.T) {
	c := test.FakeClient()
	ic := &image.Client{
		Client:    c,
		ProjectID: projectID,
	}
	errorUUIDGenerator := &errorUUIDGenerator{}
	stemcells := NewStemcells(c, projectID, defaultStorage, ic, errorUUIDGenerator, logger, &test.Timeout, false)

	_, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	gotError := err.Error()
	expectedError := "generate uuid"
	if !strings.Contains(gotError, expectedError) {
		t.Fatalf("got error '%s', expected error '%s'", gotError, expectedError)
	}
}

func TestCreateStemcell_Fail_UploadFile(t *testing.T) {
	testCases := map[string]struct {
		storage       storageService
		expectedError string
	}{
		"error on storage client setup": {
			storage:       &StorageErrorClientSetup{},
			expectedError: "set up storage client",
		},
		"error on image upload": {
			storage:       &StorageErrorUploader{},
			expectedError: "upload file to bucket",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			c := test.FakeClient()
			ic := &image.Client{
				Client:    c,
				ProjectID: projectID,
			}
			stemcells := NewStemcells(c, projectID, tc.storage, ic, uuidGen, logger, &test.Timeout, false)
			stemcells.ignoreConflict = true

			_, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
			if err == nil {
				t.Fatal("got nil, expected error")
			}

			gotError := err.Error()
			if !strings.Contains(gotError, tc.expectedError) {
				t.Fatalf("got error '%s', expected error '%s'", gotError, tc.expectedError)
			}
		})
	}
}

func TestCreateStemcell_Succeed_withImageNotCreatedByCPI(t *testing.T) {
	imageImport := createImageImport(vmconsts.ImportJobComplete)
	imageNotCreatedByCPI := createVirtualMachineImageWithoutCPILabel(defaultStemcellID.AsString())
	otherImport := createVirtualMachineImageImport(randomVersion, vmconsts.ImportJobComplete, "test-import-complete")
	c := test.FakeClient(imageImport, imageNotCreatedByCPI, otherImport)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{}, uuidGen, logger, &test.Timeout, false)
	stemcells.ignoreConflict = true

	stemcellCID, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotCID := stemcellCID.AsString()
	wantCID := defaultStemcellID.AsString()
	if gotCID != wantCID {
		t.Fatalf("got created stemcell with CID '%s', expected stemcell with CID '%s'", gotCID, wantCID)
	}
}

func TestCreateStemcell_Succeed_FallbackToSuccessfulImport_RecoverImage(t *testing.T) {
	olderImport := createVirtualMachineImageImport(defaultVersion, vmconsts.ImportJobComplete, "older-import")
	// Make it explicitly Success
	olderImport.Status.Conditions[0].Status = metav1.ConditionTrue
	olderImport.CreationTimestamp = metav1.Time{Time: time.Now().Add(-10 * time.Minute)}

	newerImport := createVirtualMachineImageImport(defaultVersion, vmconsts.ImportJobComplete, "newer-import")
	newerImport.Status.Conditions[0].Status = metav1.ConditionTrue
	newerImport.CreationTimestamp = metav1.Time{Time: time.Now()}

	imageNotCreatedByCPI := createVirtualMachineImageWithoutCPILabel(defaultStemcellID.AsString())

	c := test.FakeClient(olderImport, newerImport, imageNotCreatedByCPI)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{}, uuidGen, logger, &test.Timeout, false)
	stemcells.ignoreConflict = true

	stemcellCID, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotCID := stemcellCID.AsString()
	wantCID := "newer-import"
	if gotCID != wantCID {
		t.Fatalf("got created stemcell with CID '%s', expected stemcell with CID '%s'", gotCID, wantCID)
	}
}

func TestCreateStemcell_Fail_FallbackToSuccessfulImport_GetImageError(t *testing.T) {
	olderImport := createVirtualMachineImageImport(defaultVersion, vmconsts.ImportJobComplete, "older-import")

	olderImport.Status.Conditions[0].Status = metav1.ConditionTrue
	olderImport.CreationTimestamp = metav1.Time{Time: time.Now().Add(-10 * time.Minute)}

	newerImport := createVirtualMachineImageImport(defaultVersion, vmconsts.ImportJobComplete, "newer-import")
	newerImport.Status.Conditions[0].Status = metav1.ConditionTrue
	newerImport.CreationTimestamp = metav1.Time{Time: time.Now()}

	c := test.FakeClient(olderImport, newerImport)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageServiceErrorGetImage{}, uuidGen, logger, &test.Timeout, false)
	stemcells.ignoreConflict = true

	_, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	gotError := err.Error()
	expectedError := "getting virtual machine image newer-import"
	if !strings.Contains(gotError, expectedError) {
		t.Fatalf("got error '%s', expected error '%s'", gotError, expectedError)
	}
}

func TestCreateStemcell_Succeed_withExistingImage(t *testing.T) {
	existingImage := createVirtualMachineImageWithLabel(defaultStemcellID.AsString())
	c := test.FakeClient(existingImage)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{}, uuidGen, logger, &test.Timeout, false)
	stemcells.ignoreConflict = true

	stemcellCID, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotCID := stemcellCID.AsString()
	wantCID := defaultStemcellID.AsString()
	if gotCID != wantCID {
		t.Fatalf("got created stemcell with CID '%s', expected stemcell with CID '%s'", gotCID, wantCID)
	}
}

func TestCreateStemcell_Succeed_withMultipleExistingImages(t *testing.T) {
	version := "12345"

	olderImageName := fmt.Sprintf("stemcell-%s-%s", version, "old-uuid") // shortUUID is "aa..."
	olderImage := createVirtualMachineImageWithLabel(olderImageName)
	olderImage.CreationTimestamp = metav1.NewTime(time.Now().Add(-1 * time.Hour))

	newerImageName := fmt.Sprintf("stemcell-%s-%s", version, shortUUID)
	newerImage := createVirtualMachineImageWithLabel(newerImageName)
	newerImage.CreationTimestamp = metav1.Now()

	c := test.FakeClient(olderImage, newerImage)

	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{existingImage: newerImage}, uuidGen, logger, &test.Timeout, false)
	stemcells.ignoreConflict = true

	props := Property{
		Name:           defaultName,
		Version:        version,
		Infrastructure: defaultInfra,
		Hypervisor:     defaultHypervisor,
	}
	stemcellCloudProps, _ := convertToStemcellCloudProps(props)

	stemcellCID, err := stemcells.CreateStemcell(defaultImagePath, stemcellCloudProps)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotCID := stemcellCID.AsString()
	if gotCID != newerImageName {
		t.Fatalf("got created stemcell with CID '%s', expected stemcell with CID '%s'", gotCID, newerImageName)
	}
}

func TestCreateStemcell_ForceRefresh_withOnGoingImportInProgress(t *testing.T) {
	imageImport := createImageImport(vmconsts.ImportJobComplete)
	existingImage := createVirtualMachineImageWithoutCPILabel(defaultStemcellID.AsString())
	ongoingImport := createVirtualMachineImageImport(defaultVersion, vmconsts.ImportJobPending, "test-import-pending")
	c := test.FakeClient(imageImport, existingImage, ongoingImport)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{}, uuidGen, logger, &test.Timeout, true)
	stemcells.ignoreConflict = true

	stemcellCID, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotCID := stemcellCID.AsString()
	wantCID := ongoingImport.Name
	if gotCID != wantCID {
		t.Fatalf("got created stemcell with CID '%s', expected stemcell with CID '%s'", gotCID, wantCID)
	}
}

func TestCreateStemcell_ForceRefresh_withOtherImportsInTerminalState(t *testing.T) {
	importComplete1 := createVirtualMachineImageImport(defaultVersion, vmconsts.ImportJobComplete, "test-import-complete")
	importFailed := createVirtualMachineImageImport(defaultVersion, vmconsts.ImportJobFailed, "test-import-failed")

	c := test.FakeClient(importComplete1, importFailed)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{}, uuidGen, logger, &test.Timeout, true)
	stemcells.ignoreConflict = true

	stemcellCID, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotCID := stemcellCID.AsString()
	wantCID := defaultStemcellID.AsString()
	if gotCID != wantCID {
		t.Fatalf("got created stemcell with CID '%s', expected stemcell with CID '%s'", gotCID, wantCID)
	}
}

func TestCreateStemcell_Succeed_ForceRefresh(t *testing.T) {
	existingImageName := fmt.Sprintf("stemcell-%s-existing", defaultVersion)
	existingImage := createVirtualMachineImageWithLabel(existingImageName)

	c := test.FakeClient(existingImage)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{}, uuidGen, logger, &test.Timeout, true)
	stemcells.ignoreConflict = true

	stemcellCID, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotCID := stemcellCID.AsString()
	expectedCID := defaultStemcellID.AsString()

	if gotCID != expectedCID {
		t.Fatalf("got created stemcell with CID '%s', expected stemcell with CID '%s'", gotCID, expectedCID)
	}

	if gotCID == existingImageName {
		t.Fatalf("got created stemcell with CID '%s', expected it to be different from existing image '%s'", gotCID, existingImageName)
	}
}

func TestCreateStemcell_Fail(t *testing.T) {
	imageImport := createImageImport(vmconsts.ImportJobComplete)
	c := test.FakeClient(imageImport)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageServiceErrorCheckImage{}, uuidGen, logger, &test.Timeout, false)
	stemcells.ignoreConflict = true

	_, err := stemcells.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	expectedError := "cannot import image"
	gotError := err.Error()
	if !strings.Contains(gotError, expectedError) {
		t.Fatalf("got error '%s', expected error '%s'", gotError, expectedError)
	}
}

func TestDeleteStemcell_Succeed(t *testing.T) {
	c := test.FakeClient(defaultVMImage)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{}, uuidGen, logger, &test.Timeout, false)

	if err := stemcells.DeleteStemcell(defaultStemcellID); err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestDeleteStemcell_NotExist_Noop(t *testing.T) {
	c := test.FakeClient(defaultVMImage)
	stemcells := NewStemcells(c, projectID, defaultStorage, &mockNonExistentImageService{}, uuidGen, logger, &test.Timeout, false)

	if err := stemcells.DeleteStemcell(defaultStemcellID); err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestDeleteStemcell_Fail(t *testing.T) {
	testCases := map[string]struct {
		mockImageService imageService
		expectedErr      string
	}{
		"error on stemcell deletion": {
			mockImageService: &mockImageServiceErrorDeletion{},
			expectedErr:      "delete stemcell",
		},
		"error on stemcell deletion check": {
			mockImageService: &mockImageServiceErrorWaitDeletion{},
			expectedErr:      "timeout on waiting deletion",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			c := test.FakeClient()
			stemcells := NewStemcells(c, projectID, defaultStorage, tc.mockImageService, uuidGen, logger, &test.Timeout, false)

			err := stemcells.DeleteStemcell(defaultStemcellID)
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

func TestUploadToImageBucket_Succeed(t *testing.T) {
	c := test.FakeClient()
	ic := &image.Client{
		Client:    c,
		ProjectID: projectID,
	}
	stemcells := NewStemcells(c, projectID, defaultStorage, ic, uuidGen, logger, &test.Timeout, false)

	var props Property
	if err := defaultStemcellCloudProps.As(&props); err != nil {
		t.Fatalf("failed to parse stemcell properties: %v", err)
	}

	gotImageObjectName, err := stemcells.uploadImageToBucket(defaultImagePath, props)
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	expectedImageObjectName := fmt.Sprintf("stemcell-raw-image-%s", defaultVersion)
	if gotImageObjectName != expectedImageObjectName {
		t.Fatalf("got image object %q, expected %q", gotImageObjectName, expectedImageObjectName)
	}
}

func TestParseOSVersion_Succeed(t *testing.T) {
	testCases := map[string]struct {
		expectedOSVersion vmv1.OSName
		props             Property
	}{
		"default to Ubuntu 22.04": {
			expectedOSVersion: vmv1.Ubuntu2204,
			props:             defaultProperty,
		},
		"Ubuntu 22.04 for jammy stemcell": {
			expectedOSVersion: vmv1.Ubuntu2204,
			props: Property{
				Name:           defaultName,
				Version:        defaultVersion,
				Infrastructure: defaultInfra,
				Hypervisor:     defaultHypervisor,
				OsVersion:      "jammy",
			},
		},
		"Ubuntu 24.04 for jammy stemcell": {
			expectedOSVersion: vmv1.Ubuntu2404,
			props: Property{
				Name:           defaultName,
				Version:        defaultVersion,
				Infrastructure: defaultInfra,
				Hypervisor:     defaultHypervisor,
				OsVersion:      "noble",
			},
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			c := test.FakeClient()
			stemcells := NewStemcells(c, projectID, defaultStorage, &mockImageService{}, uuidGen, logger, &test.Timeout, false)

			gotOSVersion, err := stemcells.parseOSVersion(tc.props)
			if err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
			if gotOSVersion != tc.expectedOSVersion {
				t.Fatalf("got OS version %q, expected %q", gotOSVersion, tc.expectedOSVersion)
			}
		})
	}
}

// A mock implementation of 'uuid.Generator' that always errors out
type errorUUIDGenerator struct{}

func (e *errorUUIDGenerator) Generate() (string, error) {
	return "", errors.New("failed")
}

// A mock implementation of 'uuid.Generator' that always generates the same UUID
type staticUUIDGenerator struct{}

func (e *staticUUIDGenerator) Generate() (string, error) {
	return staticUUID, nil
}

// A mock implementation of storage interface that always succeed
type SuccessStorage struct{}

func (s *SuccessStorage) UploadFromLocalPath(opts *storage.UploadOptions) error {
	return nil
}

// A mock implementation of storage interface that always errors out with client setup error
type StorageErrorClientSetup struct{}

func (s *StorageErrorClientSetup) UploadFromLocalPath(opts *storage.UploadOptions) error {
	return errors.New("failed to set up storage client")
}

// A mock implementation of storage interface that always errors out on image upload
type StorageErrorUploader struct{}

func (s *StorageErrorUploader) UploadFromLocalPath(opts *storage.UploadOptions) error {
	return errors.New("failed to upload file to bucket")
}

// A mock implementation of image service that always succeed
type mockImageService struct {
	existingImage *vmv1.VirtualMachineImage
}

func (m *mockImageService) Get(context.Context, string) (*vmv1.VirtualMachineImage, error) {
	if m.existingImage != nil {
		return m.existingImage, nil
	}
	return createVirtualMachineImageWithLabel(defaultStemcellID.AsString()), nil
}

func (m *mockImageService) DeleteImage(context.Context, string) error {
	return nil
}
func (m *mockImageService) DeleteImageImport(context.Context, string) error {
	return nil
}
func (m *mockImageService) WaitUntilImageImportComplete(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageService) WaitUntilVMImageCreation(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageService) WaitUntilImageDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageService) WaitUntilImageImportDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}

// A mock implementation of image service that always throw NotFound error on deletion
type mockNonExistentImageService struct{}

func (m *mockNonExistentImageService) Get(context.Context, string) (*vmv1.VirtualMachineImage, error) {
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: "virtualmachine.gdc.goog", Resource: "virtualmachineimages"}, "")
}

func (m *mockNonExistentImageService) DeleteImage(context.Context, string) error {
	return apierrors.NewNotFound(schema.GroupResource{}, "")
}
func (m *mockNonExistentImageService) DeleteImageImport(context.Context, string) error {
	return apierrors.NewNotFound(schema.GroupResource{}, "")
}
func (m *mockNonExistentImageService) WaitUntilImageImportComplete(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockNonExistentImageService) WaitUntilVMImageCreation(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockNonExistentImageService) WaitUntilImageDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockNonExistentImageService) WaitUntilImageImportDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}

// A mock implementation of image service that always errors on CheckImageImportReadiness
type mockImageServiceErrorCheckImage struct{}

func (m *mockImageServiceErrorCheckImage) Get(context.Context, string) (*vmv1.VirtualMachineImage, error) {
	return defaultVMImage, nil
}

func (m *mockImageServiceErrorCheckImage) DeleteImage(context.Context, string) error {
	return nil
}
func (m *mockImageServiceErrorCheckImage) DeleteImageImport(context.Context, string) error {
	return nil
}
func (m *mockImageServiceErrorCheckImage) WaitUntilImageImportComplete(ctx context.Context, imageID string, timeout time.Duration) error {
	return errors.New("cannot import image")
}
func (m *mockImageServiceErrorCheckImage) WaitUntilVMImageCreation(ctx context.Context, imageID string, timeout time.Duration) error {
	return errors.New("cannot find the vmimage")
}
func (m *mockImageServiceErrorCheckImage) WaitUntilImageDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageServiceErrorCheckImage) WaitUntilImageImportDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}

// A mock implementation of image service that always errors out on deletion
type mockImageServiceErrorDeletion struct {
	image *vmv1.VirtualMachineImage
}

func (m *mockImageServiceErrorDeletion) Get(context.Context, string) (*vmv1.VirtualMachineImage, error) {
	// In TestDeleteMultizoneStemcells, the fake client is already populated.
	// Let the actual client.Get in addLabelToImage fetch the object.
	return m.image, nil
}

func (m *mockImageServiceErrorDeletion) DeleteImage(context.Context, string) error {
	return errors.New("failed to delete stemcell")
}
func (m *mockImageServiceErrorDeletion) DeleteImageImport(context.Context, string) error {
	return nil
}
func (m *mockImageServiceErrorDeletion) WaitUntilImageImportComplete(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageServiceErrorDeletion) WaitUntilVMImageCreation(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageServiceErrorDeletion) WaitUntilImageDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageServiceErrorDeletion) WaitUntilImageImportDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}

// A mock implementation of image service that always errors out on waiting for deletion to complete
type mockImageServiceErrorWaitDeletion struct{}

func (m *mockImageServiceErrorWaitDeletion) Get(context.Context, string) (*vmv1.VirtualMachineImage, error) {
	return defaultVMImage, nil
}

func (m *mockImageServiceErrorWaitDeletion) DeleteImage(context.Context, string) error {
	return nil
}
func (m *mockImageServiceErrorWaitDeletion) DeleteImageImport(context.Context, string) error {
	return nil
}
func (m *mockImageServiceErrorWaitDeletion) WaitUntilImageImportComplete(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageServiceErrorWaitDeletion) WaitUntilVMImageCreation(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageServiceErrorWaitDeletion) WaitUntilImageDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return errors.New("timeout on waiting deletion")
}
func (m *mockImageServiceErrorWaitDeletion) WaitUntilImageImportDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}

// Test helper
func convertToStemcellCloudProps(props Property) (apiv1.StemcellCloudProps, error) {
	jsonData, err := json.Marshal(props)
	if err != nil {
		return nil, err
	}

	return &StemcellCloudPropsImpl{
		RawMessage: json.RawMessage(jsonData),
	}, nil
}

func createImageImport(status string) *vmv1.VirtualMachineImageImport {
	return &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultStemcellID.AsString(),
			Namespace: projectID,
		},
		Status: vmv1.VirtualMachineImageImportStatus{
			Conditions: []metav1.Condition{
				metav1.Condition{
					Type:   "Ready",
					Status: metav1.ConditionTrue,
					Reason: status,
				},
			},
		},
	}
}

func createVirtualMachineImageWithoutCPILabel(version string) *vmv1.VirtualMachineImage {
	return &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:            defaultStemcellID.AsString(),
			Namespace:       projectID,
			ResourceVersion: "1",
		},
		Status: vmv1.VirtualMachineImageStatus{
			Conditions: []metav1.Condition{
				{
					Type:   "Ready",
					Status: metav1.ConditionTrue,
					Reason: "Running",
				},
			},
		},
	}
}

func createVirtualMachineImageWithLabel(vmName string) *vmv1.VirtualMachineImage {
	return &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:            vmName,
			Namespace:       projectID,
			ResourceVersion: "1",
			Labels: map[string]string{
				vmconsts.CPILabel: cpiValue,
			},
		},
		Status: vmv1.VirtualMachineImageStatus{
			Conditions: []metav1.Condition{
				{
					Type:   "Ready",
					Status: metav1.ConditionTrue,
					Reason: "Running",
				},
			},
		},
	}
}

func createVirtualMachineImageImport(version, reason string, importName string) *vmv1.VirtualMachineImageImport {
	return &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      importName,
			Namespace: projectID,
			Labels: map[string]string{
				vmconsts.CPILabel: cpiValue,
			},
		},
		Spec: vmv1.VirtualMachineImageImportSpec{
			Source: vmv1.ImageSourceReference{
				ObjectStorage: &vmv1.ImageObjectStorageSourceReference{
					ObjectName: fmt.Sprintf("stemcell-raw-image-%s", version),
				},
			},
		},
		Status: vmv1.VirtualMachineImageImportStatus{
			Conditions: []metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionFalse, Reason: reason},
			},
		},
	}
}

// A mock implementation of image service that always errors out on Get
type mockImageServiceErrorGetImage struct{}

func (m *mockImageServiceErrorGetImage) Get(context.Context, string) (*vmv1.VirtualMachineImage, error) {
	return nil, errors.New("error")
}

func (m *mockImageServiceErrorGetImage) DeleteImage(context.Context, string) error {
	return nil
}

func (m *mockImageServiceErrorGetImage) DeleteImageImport(context.Context, string) error {
	return nil
}

func (m *mockImageServiceErrorGetImage) WaitUntilImageImportComplete(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}

func (m *mockImageServiceErrorGetImage) WaitUntilVMImageCreation(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}

func (m *mockImageServiceErrorGetImage) WaitUntilImageDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}
func (m *mockImageServiceErrorGetImage) WaitUntilImageImportDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	return nil
}

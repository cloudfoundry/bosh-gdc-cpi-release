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
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	"github.com/cloudfoundry/bosh-utils/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	vmconsts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/storage"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/unit"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

const (
	// Need to import custom images to the bucket named "vm-images-bucket".
	byoBucket = "vm-images-bucket"
	cpiValue  = "bosh-cpi"
)

// sanitizeVersion replaces dots with hyphens in a stemcell version string so
// the resulting VirtualMachineImageImport / VirtualMachineImage resource name
// satisfies the GDC admission webhook constraint ("must not contain dots").
func sanitizeVersion(version string) string {
	return strings.ReplaceAll(version, ".", "-")
}

type storageService interface {
	UploadFromLocalPath(opts *storage.UploadOptions) error
}

type imageService interface {
	Get(ctx context.Context, imageID string) (*vmv1.VirtualMachineImage, error)
	DeleteImage(ctx context.Context, imageID string) error
	DeleteImageImport(ctx context.Context, imageID string) error
	WaitUntilImageImportComplete(ctx context.Context, imageID string, timeout time.Duration) error
	WaitUntilVMImageCreation(ctx context.Context, imageID string, timeout time.Duration) error
	WaitUntilImageDeleted(ctx context.Context, imageID string, timeout time.Duration) error
	WaitUntilImageImportDeleted(ctx context.Context, imageID string, timeout time.Duration) error
}

type Stemcells struct {
	cloudClient    client.Client
	storage        storageService
	imageService   imageService
	projectID      string
	uuidGen        uuid.Generator
	logger         cfboshlog.Logger
	ignoreConflict bool // when opt-in, ignore image import conflict
	timeout        *config.WaitTimeout
}

func NewStemcells(cloudClient client.Client, projectID string, storage storageService, imageService imageService, uuidGen uuid.Generator, logger cfboshlog.Logger, timeout *config.WaitTimeout) *Stemcells {
	return &Stemcells{
		cloudClient:    cloudClient,
		storage:        storage,
		imageService:   imageService,
		projectID:      projectID,
		uuidGen:        uuidGen,
		logger:         logger,
		ignoreConflict: false,
		timeout:        timeout,
	}
}

// CreateStemcell only supports the import of stemcell image from the local path. The chosen version of stemcell needs to
// be downloaded and stored in the working machine that invokes the CPI method.
// Each call always creates a new, independently-owned VirtualMachineImage so that separate CPI instances
// (e.g. create-env and the director) can safely delete their own stemcell without affecting the other.
// The GCS object upload is idempotent (same version → same object name) to avoid redundant byte transfers.
func (s *Stemcells) CreateStemcell(imagePath string, stemcellCloudProps apiv1.StemcellCloudProps) (apiv1.StemcellCID, error) {
	if len(imagePath) == 0 {
		return apiv1.StemcellCID{}, errors.New("must provide non-empty imagePath")
	}

	var props Property
	if err := stemcellCloudProps.As(&props); err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("parse stemcell properties: %w", err)
	}

	if err := props.validate(); err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("there are invalid stemcell properties: %w", err)
	}

	originalUUID, err := s.uuidGen.Generate()
	if err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("generate uuid: %w", err)
	}
	// make the stemcell ID shorter to avoid length validation error during image import.
	// given the scale of images in object storage bucket, the collision rate is very low,
	// so it's safe to truncate the first 8 chars of UUID
	stemcellUUID := strings.Split(originalUUID, "-")[0]
	stemcellCID := apiv1.NewStemcellCID(
		fmt.Sprintf("stemcell-%s-%s", sanitizeVersion(props.Version), stemcellUUID),
	)

	// Step 1: Upload Image to GDC Bucket, idempotent (skips re-upload if object already exists)
	imageObjectName, err := s.uploadImageToBucket(imagePath, props)
	if err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("upload stemcell image from local path %s: %w", imagePath, err)
	}

	// Step 2: Import as a new, uniquely-named VirtualMachineImage owned by this CPI call
	ctx := context.Background()
	if err := s.importImage(ctx, imageObjectName, stemcellCID, props); err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("import stemcell image: %w", err)
	}

	return stemcellCID, nil
}

// TODO: migrate to use VMM's formal DeleteVirtualMachineImage API when ready
func (s *Stemcells) DeleteStemcell(cid apiv1.StemcellCID) error {
	return s.deleteStemcell(context.Background(), cid.AsString())
}

// deleteStemcell removes the VirtualMachineImage and the VirtualMachineImageImport
// that produced it. The image is deleted and drained first so its cascading
// DataVolume/PVC cleanup finishes before the import that owns it goes away.
// Each step tolerates IsNotFound and still proceeds to the next, so a partially
// completed prior delete converges instead of leaking the remaining resource.
func (s *Stemcells) deleteStemcell(ctx context.Context, stemcellID string) error {
	timeout := time.Duration(s.timeout.StemcellDeletion) * time.Minute

	if err := s.imageService.DeleteImage(ctx, stemcellID); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stemcell: %w", err)
		}
		s.logger.Info("DeleteStemcell", "stemcell %q doesn't exist", stemcellID)
	} else if err := s.imageService.WaitUntilImageDeleted(ctx, stemcellID, timeout); err != nil {
		return fmt.Errorf("delete stemcell %q: %w", stemcellID, err)
	}

	if err := s.imageService.DeleteImageImport(ctx, stemcellID); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stemcell image import: %w", err)
		}
		s.logger.Info("DeleteStemcell", "stemcell image import %q doesn't exist", stemcellID)
	} else if err := s.imageService.WaitUntilImageImportDeleted(ctx, stemcellID, timeout); err != nil {
		return fmt.Errorf("delete stemcell image import %q: %w", stemcellID, err)
	}

	return nil
}

// Import stemcell image as a usable bootable image for GDCH VMs
func (s *Stemcells) importImage(ctx context.Context, imageObjectName string, cid apiv1.StemcellCID, props Property) (err error) {
	osVersion, err := s.parseOSVersion(props)
	if err != nil {
		return err
	}

	stemcellID := cid.AsString()
	s.logger.Info("importImage", "Importing stemcell image as %s", stemcellID)

	imageImport := &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stemcellID,
			Namespace: s.projectID,
			Labels: map[string]string{
				vmconsts.CPILabel: cpiValue,
			},
		},
		Spec: vmv1.VirtualMachineImageImportSpec{
			Source: vmv1.ImageSourceReference{
				ObjectStorage: &vmv1.ImageObjectStorageSourceReference{
					ObjectName: imageObjectName,
					BucketRef: corev1.LocalObjectReference{
						Name: byoBucket,
					},
				},
			},
			ImageMetadata: vmv1.ImageMetadataInput{
				Name:            stemcellID,
				OperatingSystem: osVersion,
				MinimumDiskSize: resource.NewQuantity(15*unit.GiB, resource.BinarySI),
			},
		},
	}

	// VMM VirtualMachineImageImport API throws error on request conflict.
	// For unit tests, use 'GenerateName' rather than 'Name' to ignore image import conflict
	if s.ignoreConflict {
		imageImport.ObjectMeta = metav1.ObjectMeta{
			GenerateName: stemcellID,
			Namespace:    s.projectID,
			Labels: map[string]string{
				vmconsts.CPILabel: cpiValue,
			},
		}
	}

	if err := s.cloudClient.Create(ctx, imageImport); err != nil {
		return fmt.Errorf("create import of stemcell image as %s: %w", stemcellID, err)
	}

	// The import, and the image it produces, now exist in GDC but BOSH has no CID
	// for them until this call returns successfully. Nothing reclaims them later,
	// so any failure from here on must clean up after itself.
	defer func() {
		if err != nil {
			s.rollbackImport(stemcellID)
		}
	}()

	if err := s.imageService.WaitUntilImageImportComplete(ctx, stemcellID, time.Duration(s.timeout.StemcellCreation)*time.Minute); err != nil {
		return fmt.Errorf("import stemcell image as %s: %w", stemcellID, err)
	}

	// Wait for the actual VirtualMachineImage to be created.
	if err := s.imageService.WaitUntilVMImageCreation(ctx, stemcellID, time.Duration(s.timeout.StemcellCreation)*time.Minute); err != nil {
		return fmt.Errorf("waiting for virtual machine image %q creation: %w", stemcellID, err)
	}

	// Add the custom label and update the resource.
	if err := s.addLabelToImage(ctx, stemcellID); err != nil {
		return fmt.Errorf("adding label to virtual machine image %q: %w", stemcellID, err)
	}

	s.logger.Info("importImage", "Successfully imported stemcell image as %s", stemcellID)
	return nil
}

// rollbackImport makes a best-effort attempt to remove the image and image import
// left behind by a CreateStemcell call that will not return a CID to BOSH. It
// never reports failure: the caller is already returning an error and the
// original cause must not be masked.
func (s *Stemcells) rollbackImport(stemcellID string) {
	// Use a fresh context: the caller's may already be past its deadline, and
	// cleanup must not inherit the StemcellCreation timeout. The budget covers
	// both of deleteStemcell's sequential waits, so a slow but healthy delete is
	// not cut short and reported as needing manual cleanup.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(s.timeout.StemcellDeletion)*time.Minute)
	defer cancel()

	s.logger.Warn("importImage", "import of stemcell %q failed, cleaning up partial resources", stemcellID)
	if err := s.deleteStemcell(ctx, stemcellID); err != nil {
		s.logger.Warn("importImage", "cleanup of stemcell %q failed, manual cleanup may be required: %v", stemcellID, err)
	}
}

// Upload stemcell image from local to S3 bucket in GDCH
//
// The object name is keyed on the stemcell version alone, so concurrent CPI
// instances importing the same version share one upload rather than each paying
// for a slow GDC transfer. This assumes a given version always means identical
// bytes: a tarball rebuilt under an existing version will not be re-uploaded.
func (s *Stemcells) uploadImageToBucket(imagePath string, props Property) (string, error) {
	imageFileName := filepath.Base(imagePath)

	imageObjectName := fmt.Sprintf("stemcell-raw-image-%s", props.Version)
	uploadOpts := &storage.UploadOptions{
		ObjectName: imageObjectName,
		FilePath:   imagePath,
		BucketName: byoBucket,
	}

	if err := s.storage.UploadFromLocalPath(uploadOpts); err != nil {
		return "", fmt.Errorf("upload stemcell image %q from %q to object storage bucket %q: %w", imageFileName, imagePath, byoBucket, err)
	}

	s.logger.Info("uploadImageToBucket", "Successfully uploaded stemcell image %q as object %q to bucket %q", imageFileName, imageObjectName, byoBucket)
	return imageObjectName, nil
}

func (s *Stemcells) addLabelToImage(ctx context.Context, stemcellID string) error {
	image, err := s.imageService.Get(ctx, stemcellID)
	if err != nil {
		return fmt.Errorf("getting virtual machine image %q: %w", stemcellID, err)
	}
	if image.Labels != nil && image.Labels[vmconsts.CPILabel] == cpiValue {
		s.logger.Info("CreateStemcell", "Custom label already exists on VirtualMachineImage %q", image.Name)
		return nil
	}

	s.logger.Info("CreateStemcell", "Adding custom label to VirtualMachineImage %q", image.Name)
	if image.Labels == nil {
		image.Labels = make(map[string]string)
	}
	image.Labels[vmconsts.CPILabel] = cpiValue

	if err := s.cloudClient.Update(ctx, image); err != nil {
		return fmt.Errorf("updating virtual machine image %q with new label: %w", image.Name, err)
	}

	s.logger.Info("CreateStemcell", "Successfully added custom label to VirtualMachineImage %q", image.Name)
	return nil
}

func (s *Stemcells) parseOSVersion(props Property) (vmv1.OSName, error) {
	// Default to Ubuntu 22.04 OS tag
	osVersion := vmv1.Ubuntu2204

	if props.OsVersion == "noble" {
		osVersion = vmv1.Ubuntu2404
	}

	return osVersion, nil
}

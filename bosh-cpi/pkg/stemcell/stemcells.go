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
	"regexp"
	"sort"
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
	// Need to import custom images to the bucket named "vm-images-bucket", http://shortn/_MtP1GLY3Io
	byoBucket = "vm-images-bucket"
	cpiValue  = "bosh-cpi"
)

var (
	cpiLabels         = map[string]string{vmconsts.CPILabel: cpiValue}
	stemcellNameRegex = regexp.MustCompile(`^stemcell-([a-zA-Z0-9.-]+)-[a-zA-Z0-9-]+$`)
)

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
	cloudClient        client.Client
	storage            storageService
	imageService       imageService
	projectID          string
	uuidGen            uuid.Generator
	logger             cfboshlog.Logger
	ignoreConflict     bool // when opt-in, ignore image import conflict
	timeout            *config.WaitTimeout
	refreshImageImport bool
}

func NewStemcells(cloudClient client.Client, projectID string, storage storageService, imageService imageService, uuidGen uuid.Generator, logger cfboshlog.Logger, timeout *config.WaitTimeout, refreshImageImport bool) *Stemcells {
	return &Stemcells{
		cloudClient:        cloudClient,
		storage:            storage,
		imageService:       imageService,
		projectID:          projectID,
		uuidGen:            uuidGen,
		logger:             logger,
		ignoreConflict:     false,
		timeout:            timeout,
		refreshImageImport: refreshImageImport,
	}
}

// CreateStemcell only supports the import of stemcell image from the local path. The chosen version of stemcell needs to
// be downloaded and stored in the working machine that invokes the CPI method
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

	if !s.refreshImageImport {
		s.logger.Info("CreateStemcell", "refreshImageImport is unset; searching for existing stemcell with version: %s", props.Version)
		image, err := s.findExistingImage(context.Background(), props.Version)
		if err != nil {
			return apiv1.StemcellCID{}, fmt.Errorf("find existing image: %w", err)
		}
		if image != nil {
			s.logger.Info("CreateStemcell", "found an existing stemcell with version %s created by CPI; reusing this stemcell.", props.Version)
			return apiv1.NewStemcellCID(image.Name), nil
		}

		s.logger.Info("CreateStemcell", "no matching images found for stemcell version %s, initiating search for successful image imports", props.Version)
		successfulImport, err := s.findMostRecentSuccessfulImport(context.Background(), props.Version)
		if err != nil {
			return apiv1.StemcellCID{}, fmt.Errorf("find successful image import: %w", err)
		}
		if successfulImport != nil {
			_, err := s.imageService.Get(context.Background(), successfulImport.Name)
			if err == nil {
				s.logger.Info("CreateStemcell", "found successful import %s with existing image, adding label", successfulImport.Name)
				if err := s.addLabelToImage(context.Background(), successfulImport.Name); err != nil {
					return apiv1.StemcellCID{}, fmt.Errorf("adding label to existing image %s: %w", successfulImport.Name, err)
				}
				return apiv1.NewStemcellCID(successfulImport.Name), nil
			} else if !apierrors.IsNotFound(err) {
				return apiv1.StemcellCID{}, fmt.Errorf("getting virtual machine image %s: %w", successfulImport.Name, err)
			}
			s.logger.Info("CreateStemcell", "successful import %s found but corresponding image was not found in project %s, new image import will be triggered", successfulImport.Name, s.projectID)
		}
	}
	// Check for any concurrent image imports
	ongoingBYO, ongoingBYOErr := s.findConcurrentImportInProgress(context.Background(), props.Version)

	if ongoingBYOErr != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("error finding other ongoing VirtualMachineImageImport processes: %w", ongoingBYOErr)
	}

	if ongoingBYO != nil {
		s.logger.Info("CreateStemcell", "Found an ongoing VirtualMachineImageImport process CPI will be using this stemcell: %w", ongoingBYO.Name)
		if err := s.imageService.WaitUntilImageImportComplete(context.Background(), ongoingBYO.Name, time.Duration(s.timeout.StemcellCreation)*time.Minute); err != nil {
			return apiv1.StemcellCID{}, fmt.Errorf("error occurred while waiting for an ongoing VirtualMachineImageImport Process %w", err)
		}
		return apiv1.NewStemcellCID(ongoingBYO.Name), nil
	}

	original_uuid, err := s.uuidGen.Generate()
	if err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("generate uuid: %w", err)
	}
	// make the stemcell ID shorter to avoid length validation error during image import.
	// given the scale of images in object storage bucket, the collision rate is very low,
	// so it's safe to truncate the first 8 chars of UUID
	stemcellUUID := strings.Split(original_uuid, "-")[0]
	stemcellCID := apiv1.NewStemcellCID(
		fmt.Sprintf("stemcell-%s-%s", props.Version, stemcellUUID),
	)

	// Step 1: Upload Image to GCS Bucket, where in the storage class makes sure the idempotency is maintainted at this step
	// Upload logic start
	imageObjectName, err := s.uploadImageToBucket(imagePath, props)
	if err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("upload stemcell image from local path %s: %w", imagePath, err)
	}

	// Step 2: The VM Image import step takes place based on the stemcell image that was uploaded and this is where idempotency is required to be achieved
	// VirtualMachineImageImport Creation logic start
	ctx := context.Background()
	if err := s.importImage(ctx, imageObjectName, stemcellCID, props); err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("import stemcell image: %w", err)
	}

	return stemcellCID, nil
}

// TODO(b/309450687): migrate to use VMM's formal DeleteVirtualMachineImage API when ready (b/305796934)
func (s *Stemcells) DeleteStemcell(cid apiv1.StemcellCID) error {
	ctx := context.Background()
	stemcellID := cid.AsString()
	if err := s.imageService.DeleteImage(ctx, stemcellID); err != nil {
		if apierrors.IsNotFound(err) {
			s.logger.Info("DeleteStemcell", "stemcell %q doesn't exist", stemcellID)
			return nil
		}
		return fmt.Errorf("delete stemcell: %w", err)
	}

	if err := s.imageService.DeleteImageImport(ctx, stemcellID); err != nil {
		if apierrors.IsNotFound(err) {
			s.logger.Info("DeleteStemcell", "stemcell image import %q doesn't exist", stemcellID)
			return nil
		}
		return fmt.Errorf("delete stemcell image import: %w", err)
	}

	if err := s.imageService.WaitUntilImageDeleted(ctx, stemcellID, time.Duration(s.timeout.StemcellDeletion)*time.Minute); err != nil {
		return fmt.Errorf("delete stemcell %q: %w", cid, err)
	}

	if err := s.imageService.WaitUntilImageImportDeleted(ctx, stemcellID, time.Duration(s.timeout.StemcellDeletion)*time.Minute); err != nil {
		return fmt.Errorf("delete stemcell image import %q: %w", stemcellID, err)
	}

	return nil
}

// Import stemcell image as a usable bootable image for GDCH VMs
func (s *Stemcells) importImage(ctx context.Context, imageObjectName string, cid apiv1.StemcellCID, props Property) error {
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

	// VMM VirtuamMachineImageImport API throws error on request conflict, http://shortn/_YRJEaIbdxw.
	// For unit tests, use 'GenerateName' rather than 'Name' to ignore image import conflict
	if s.ignoreConflict {
		imageImport.ObjectMeta = metav1.ObjectMeta{
			GenerateName: stemcellID,
			Namespace:    s.projectID,
		}
	}

	if err := s.cloudClient.Create(ctx, imageImport); err != nil {
		return fmt.Errorf("create import of stemcell image as %s: %w", stemcellID, err)
	}

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

// Upload stemcell image from local to S3 bucket in GDCH
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

func (s *Stemcells) findExistingImage(ctx context.Context, version string) (*vmv1.VirtualMachineImage, error) {
	imageList := &vmv1.VirtualMachineImageList{}

	if err := s.cloudClient.List(ctx, imageList, client.InNamespace(s.projectID), client.MatchingLabels(cpiLabels)); err != nil {
		return nil, fmt.Errorf("listing virtual machine images: %w", err)
	}

	sort.Slice(imageList.Items, func(i, j int) bool {
		return imageList.Items[i].CreationTimestamp.Time.After(imageList.Items[j].CreationTimestamp.Time)
	})

	for _, image := range imageList.Items {
		matches := stemcellNameRegex.FindStringSubmatch(image.Name)
		if len(matches) != 2 {
			continue
		}
		if matches[1] == version {
			return &image, nil
		}
	}
	return nil, nil
}

func (s *Stemcells) parseOSVersion(props Property) (vmv1.OSName, error) {
	// Default to Ubuntu 22.04 OS tag
	osVersion := vmv1.Ubuntu2204

	if props.OsVersion == "noble" {
		osVersion = vmv1.Ubuntu2404
	}

	return osVersion, nil
}

func (s *Stemcells) findConcurrentImportInProgress(ctx context.Context, version string) (*vmv1.VirtualMachineImageImport, error) {
	importList := &vmv1.VirtualMachineImageImportList{}
	if err := s.cloudClient.List(ctx, importList, client.InNamespace(s.projectID), client.MatchingLabels(cpiLabels)); err != nil {
		s.logger.Error("findConcurrentImportInProgress", "Failed to list VirtualMachineImageImports: %v", err)
		return nil, err
	}

	for _, item := range importList.Items {
		if item.Spec.Source.ObjectStorage != nil {
			// objectName is expected to be in format "stemcell-raw-image-<version>"
			importVersion := strings.TrimPrefix(item.Spec.Source.ObjectStorage.ObjectName, "stemcell-raw-image-")
			if importVersion == version {
				for _, condition := range item.Status.Conditions {
					if condition.Type == "Ready" && condition.Status == metav1.ConditionFalse {
						switch condition.Reason {
						case vmconsts.ImportJobComplete, vmconsts.ImportJobFailed:
							continue
						default:
							return &item, nil
						}
					}
				}
			}
		}
	}

	return nil, nil
}

func (s *Stemcells) findMostRecentSuccessfulImport(ctx context.Context, version string) (*vmv1.VirtualMachineImageImport, error) {
	importList := &vmv1.VirtualMachineImageImportList{}
	if err := s.cloudClient.List(ctx, importList, client.InNamespace(s.projectID), client.MatchingLabels(cpiLabels)); err != nil {
		s.logger.Error("findMostRecentSuccessfulImport", "Failed to list VirtualMachineImageImports: %v", err)
		return nil, err
	}

	var successfulImports []vmv1.VirtualMachineImageImport

	for _, item := range importList.Items {
		if item.Spec.Source.ObjectStorage != nil {
			importVersion := strings.TrimPrefix(item.Spec.Source.ObjectStorage.ObjectName, "stemcell-raw-image-")
			if importVersion == version {
				for _, condition := range item.Status.Conditions {
					if condition.Type == "Ready" {
						if condition.Status == metav1.ConditionTrue || condition.Reason == vmconsts.ImportJobComplete {
							successfulImports = append(successfulImports, item)
							break
						}
					}
				}
			}
		}
	}

	if len(successfulImports) == 0 {
		return nil, nil
	}

	sort.Slice(successfulImports, func(i, j int) bool {
		return successfulImports[i].CreationTimestamp.Time.After(successfulImports[j].CreationTimestamp.Time)
	})

	return &successfulImports[0], nil
}

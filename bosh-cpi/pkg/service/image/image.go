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

// Package image contains logic to interact with GDCH APIs for
// operations on virtual machine images.
package image

import (
	"context"
	"fmt"
	"time"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vmconsts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

type waiter interface {
	Until(ctx context.Context, check wait.ConditionChecker, name string, obj client.Object, timeout time.Duration) error
	UntilDeleted(ctx context.Context, name string, obj client.Object, timeout time.Duration) error
}

type Client struct {
	Client client.Client
	// The GDCH project in which images are stored.
	ProjectID string
	Logger    cfboshlog.Logger
	Waiter    waiter
}

// Get feteches the `VirtualMachineImage` object from the GDCH API based on the
// specified image ID.
func (c *Client) Get(ctx context.Context, imageID string) (*vmv1.VirtualMachineImage, error) {
	return c.get(ctx, imageID)
}

// Has checks the existence of a VirtualMachineImage.
func (c *Client) Has(ctx context.Context, imageID string) (bool, error) {
	if _, err := c.get(ctx, imageID); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		} else {
			return false, err
		}
	}
	return true, nil
}

func (c *Client) Delete(ctx context.Context, imageID string) error {
	vmi := &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageID,
			Namespace: c.ProjectID,
		},
	}

	if err := c.Client.Delete(ctx, vmi); err != nil {
		return fmt.Errorf("delete VirtualMachineImage %q: %w", imageID, err)
	}

	return nil
}

// Wait and confirm the image import completes. If the image import fails, there will not be associated image that's ready to use for VMs.
func (c *Client) WaitUntilImageImportComplete(ctx context.Context, imageID string, timeout time.Duration) error {
	imageImportStatusChecker := func(obj client.Object) (bool, error) {
		imageImport, ok := obj.(*vmv1.VirtualMachineImageImport)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachineImageImport: %v", obj)
		}

		condition := meta.FindStatusCondition(imageImport.Status.Conditions, "Ready")
		if condition == nil {
			c.Logger.Info("importImage", "status of image %s has not been populated yet", imageID)
			return false, nil
		}

		switch condition.Reason {
		case vmconsts.ImportJobComplete:
			c.Logger.Info("importImage", "Import of image %s completed", imageID)
			return true, nil
		case vmconsts.ImportJobFailed, vmconsts.TranslationJobFailed:
			c.Logger.Error("importImage", "Import of image %s permanently failed on status: %s", imageID, condition.Reason)
			return false, fmt.Errorf("import of image %s failed permanently", imageID)
		default:
			c.Logger.Info("importImage", "Import of image %s is still in progress", imageID)
			return false, nil
		}
	}

	if err := c.Waiter.Until(ctx, imageImportStatusChecker, imageID, &vmv1.VirtualMachineImageImport{}, timeout); err != nil {
		return fmt.Errorf("wait until image %q is imported: %w", imageID, err)
	}
	return nil
}

func (c *Client) WaitUntilVMImageCreation(ctx context.Context, imageID string, timeout time.Duration) error {
	vmImageChecker := func(obj client.Object) (bool, error) {
		return c.Has(ctx, imageID)
	}
	if err := c.Waiter.Until(ctx, vmImageChecker, imageID, &vmv1.VirtualMachineImage{}, timeout); err != nil {
		return fmt.Errorf("wait until image %q is created: %w", imageID, err)
	}
	return nil
}

// Wait and confirm deletion is complete.
func (c *Client) WaitUntilImageDeleted(ctx context.Context, imageID string, timeout time.Duration) error {
	if err := c.Waiter.UntilDeleted(ctx, imageID, &vmv1.VirtualMachineImage{}, timeout); err != nil {
		return fmt.Errorf("wait until image %q is deleted: %w", imageID, err)
	}

	return nil
}

func (c *Client) get(ctx context.Context, imageID string) (*vmv1.VirtualMachineImage, error) {
	image := &vmv1.VirtualMachineImage{}
	key := client.ObjectKey{
		Name:      imageID,
		Namespace: c.ProjectID,
	}
	if err := c.Client.Get(ctx, key, image); err != nil {
		return nil, fmt.Errorf("get VirtualMachineImage %v: %w", key, err)
	}
	return image, nil
}

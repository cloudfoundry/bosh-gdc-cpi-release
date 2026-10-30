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

package image_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vmconsts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait/mock"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/image"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

const (
	projectID = "test-project"
	imageID   = "image"
)

var (
	ignoreTypeMeta = cmpopts.IgnoreFields(metav1.TypeMeta{}, "Kind", "APIVersion")
)

func TestGet(t *testing.T) {
	vmi1 := vmi("vmi1", "project-1")
	c := test.FakeClient(vmi1)
	ic := image.Client{
		Client:    c,
		ProjectID: "project-1",
	}

	ctx := context.Background()
	image, err := ic.Get(ctx, "vmi1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := cmp.Diff(vmi1, image, ignoreTypeMeta); diff != "" {
		t.Fatalf("unexpected VirtualMachineImage, (-want +got):\n%s", diff)
	}
}

func TestGetError(t *testing.T) {
	c := test.FakeClient(vmi("vm1-project-2", "project-2"))
	ic := image.Client{
		Client:    c,
		ProjectID: "project-1",
	}
	testCases := map[string]struct {
		imageID string
	}{
		"image doesn't exist": {
			imageID: "non-existent-vmi",
		},
		"image in a different project": {
			imageID: "vm1-project-2",
		},
	}

	ctx := context.Background()
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			_, err := ic.Get(ctx, tc.imageID)
			if err == nil {
				t.Fatal("expecting an error but got nil")
			}
		})
	}
}
func TestHas(t *testing.T) {
	vmi1 := vmi("vmi-1-project-1", "project-1")
	vmi2 := vmi("vmi-2-project-2", "project-2")
	c := test.FakeClient(vmi1, vmi2)
	ic := image.Client{
		Client:    c,
		ProjectID: "project-1",
	}

	ctx := context.Background()
	testCases := map[string]struct {
		expected bool
		imageID  string
	}{
		"has VMI": {
			expected: true,
			imageID:  "vmi-1-project-1",
		},
		"non-existing VMI": {
			expected: false,
			imageID:  "non-existing",
		},
		"VMI in different project": {
			expected: false,
			imageID:  "vmi-2-project-2",
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			has, err := ic.Has(ctx, tc.imageID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.expected != has {
				t.Fatalf("got %t but want %t", has, tc.expected)
			}
		})
	}
}

func TestHasError(t *testing.T) {
	// Use a client without scheme to trigger error
	fc := test.FakeInvalidClient()
	ic := image.Client{
		Client:    fc,
		ProjectID: "project-1",
	}
	if _, err := ic.Has(context.Background(), "vmi1"); err == nil {
		t.Error("expecting an error but got nil")
	}
}

func TestDelete(t *testing.T) {
	vmi1 := vmi("vmi-id", "project")
	c := test.FakeClient(vmi1)
	ic := image.Client{
		Client:    c,
		ProjectID: "project",
	}

	ctx := context.Background()
	if err := ic.Delete(ctx, "vmi-id"); err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	has, err := ic.Has(ctx, "vmi-id")
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}
	if has == true {
		t.Fatal("image still existes, expected deleted")
	}
}

func TestDeleteError(t *testing.T) {
	fc := test.FakeInvalidClient()
	ic := image.Client{
		Client:    fc,
		ProjectID: "project",
	}
	if err := ic.Delete(context.Background(), "vmi-id"); err == nil {
		t.Error("got nil, expected error")
	}
}

func vmi(name, namespace string) *vmv1.VirtualMachineImage {
	return &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

func TestDeleteImageImport(t *testing.T) {
	ii := &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vmi-id",
			Namespace: "project",
		},
	}
	c := test.FakeClient(ii)
	ic := image.Client{
		Client:    c,
		ProjectID: "project",
	}

	ctx := context.Background()
	if err := ic.DeleteImageImport(ctx, "vmi-id"); err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	got := &vmv1.VirtualMachineImageImport{}
	err := c.Get(ctx, client.ObjectKey{Name: "vmi-id", Namespace: "project"}, got)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("import still exists, expected deleted (err=%v)", err)
	}
}

func TestDeleteImageImportNotFound(t *testing.T) {
	c := test.FakeClient()
	ic := image.Client{
		Client:    c,
		ProjectID: "project",
	}
	if err := ic.DeleteImageImport(context.Background(), "missing"); err != nil {
		t.Errorf("got %v, expected nil for a non-existent import", err)
	}
}

func TestDeleteImageImportError(t *testing.T) {
	fc := test.FakeInvalidClient()
	ic := image.Client{
		Client:    fc,
		ProjectID: "project",
	}
	if err := ic.DeleteImageImport(context.Background(), "vmi-id"); err == nil {
		t.Error("got nil, expected error")
	}
}

func TestCheckImageImportReadiness_Succeed(t *testing.T) {
	ii := &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageID,
			Namespace: projectID,
		},
	}
	ts := defaultTestSetup(ii)

	// set server side action to update image import with "ImportJobComplete" status
	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      imageID,
			Namespace: projectID,
		}
		if err := c.Get(ctx, key, ii); err != nil {
			return fmt.Errorf("get image import: %v", err)
		}

		ii.Status = createImageImport(vmconsts.ImportJobComplete).Status
		if err := c.Update(ctx, ii); err != nil {
			return fmt.Errorf("update image import: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineImageImport"] = action
	ts.w.TargetObjects["VirtualMachineImageImport"] = &vmv1.VirtualMachineImageImport{}

	if err := ts.ic.WaitUntilImageImportComplete(context.Background(), imageID, time.Second); err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestCheckImageImportReadiness_Fail(t *testing.T) {
	imageImportPending := createImageImport(vmconsts.ImportJobPending)
	imageImportFailed := createImageImport(vmconsts.ImportJobFailed)
	imageImportTransFailed := createImageImport(vmconsts.TranslationJobFailed)
	imageImportNilCond := &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageID,
			Namespace: projectID,
		},
	}
	otherImageImport := &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-image",
			Namespace: projectID,
		},
		Status: vmv1.VirtualMachineImageImportStatus{
			Conditions: []metav1.Condition{
				metav1.Condition{
					Type:   "Ready",
					Status: metav1.ConditionTrue,
					Reason: vmconsts.ImportJobComplete,
				},
			},
		},
	}

	testCases := map[string]struct {
		imageImport *vmv1.VirtualMachineImageImport
		expectedErr string
	}{
		"Fail on image import not found": {
			imageImport: otherImageImport,
			expectedErr: "not found",
		},
		"Fail on pending image import": {
			imageImport: imageImportPending,
			expectedErr: "wait until image",
		},
		"Fail on empty condition": {
			imageImport: imageImportNilCond,
			expectedErr: "wait until image",
		},
		"Fail on failed job": {
			imageImport: imageImportFailed,
			expectedErr: "import of image image failed permanently",
		},
		"Fail on failed translation job": {
			imageImport: imageImportTransFailed,
			expectedErr: "import of image image failed permanently",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			ii := &vmv1.VirtualMachineImageImport{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tc.imageImport.Name,
					Namespace: projectID,
				},
			}
			ts := defaultTestSetup(ii)

			// set server side action to update image import with specified status
			action := func(ctx context.Context, c client.Client) error {
				key := client.ObjectKey{
					Name:      imageID,
					Namespace: projectID,
				}
				if err := c.Get(ctx, key, ii); err != nil {
					return fmt.Errorf("get image import: %v", err)
				}

				ii.Status = tc.imageImport.Status
				if err := c.Update(ctx, ii); err != nil {
					return fmt.Errorf("update image import: %v", err)
				}
				return nil
			}
			ts.w.ServerSideActions["VirtualMachineImageImport"] = action
			ts.w.TargetObjects["VirtualMachineImageImport"] = &vmv1.VirtualMachineImageImport{}

			err := ts.ic.WaitUntilImageImportComplete(context.Background(), imageID, time.Second)
			if err == nil {
				t.Fatal("got nil, expected error")
			}

			gotError := err.Error()
			if !strings.Contains(gotError, tc.expectedErr) {
				t.Fatalf("got error %q, expected error %q", gotError, tc.expectedErr)
			}
		})
	}
}

func TestWaitUntilVMImageCreation_Succeed(t *testing.T) {
	vmImage := &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageID,
			Namespace: projectID,
		},
	}
	ts := defaultTestSetup(vmImage)
	ts.w.TargetObjects["VirtualMachineImage"] = &vmv1.VirtualMachineImage{}

	if err := ts.ic.WaitUntilVMImageCreation(context.Background(), imageID, time.Second); err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestWaitUntilVMImageCreation_Fail(t *testing.T) {
	ts := defaultTestSetup()

	action := func(ctx context.Context, c client.Client) error {
		return errors.New("error")
	}
	ts.w.ServerSideActions["VirtualMachineImage"] = action
	ts.w.TargetObjects["VirtualMachineImage"] = &vmv1.VirtualMachineImage{}

	err := ts.ic.WaitUntilVMImageCreation(context.Background(), imageID, time.Second)
	if err == nil {
		t.Fatal("got nil, expected error")
	}
}

func TestWaitUntilImageDeletion_Succeed(t *testing.T) {
	ii := &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageID,
			Namespace: projectID,
		},
	}
	ts := defaultTestSetup(ii)

	// set server side action to delete image
	action := func(ctx context.Context, c client.Client) error {
		if err := c.Delete(ctx, ii); err != nil {
			return fmt.Errorf("get image import: %v", err)
		}

		return nil
	}
	ts.w.ServerSideActions["VirtualMachineImage"] = action
	ts.w.TargetObjects["VirtualMachineImage"] = &vmv1.VirtualMachineImage{}

	if err := ts.ic.WaitUntilImageDeleted(context.Background(), imageID, time.Second); err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestWaitUntilImageDeletion_Fail(t *testing.T) {
	i := &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageID,
			Namespace: projectID,
		},
	}
	ts := defaultTestSetup(i)

	// set server side action throwing error
	action := func(ctx context.Context, c client.Client) error {
		return errors.New("error")
	}
	ts.w.ServerSideActions["VirtualMachineImage"] = action
	ts.w.TargetObjects["VirtualMachineImage"] = &vmv1.VirtualMachineImage{}

	err := ts.ic.WaitUntilImageDeleted(context.Background(), imageID, time.Second)
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	expectedError := "service side action"
	gotError := err.Error()
	if !strings.Contains(gotError, expectedError) {
		t.Fatalf("got error %q, expected error %q", gotError, expectedError)
	}
}

func TestWaitUntilImageDeletion_Fail_Timeout(t *testing.T) {
	i := &vmv1.VirtualMachineImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageID,
			Namespace: projectID,
		},
	}
	ts := defaultTestSetup(i)

	// set server side action doing nothing about deletion
	action := func(ctx context.Context, c client.Client) error {
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineImage"] = action
	ts.w.TargetObjects["VirtualMachineImage"] = &vmv1.VirtualMachineImage{}

	err := ts.ic.WaitUntilImageDeleted(context.Background(), imageID, time.Second)
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	expectedError := "is not deleted"
	gotError := err.Error()
	if !strings.Contains(gotError, expectedError) {
		t.Fatalf("got error %q, expected error %q", gotError, expectedError)
	}
}

func createImageImport(status string) *vmv1.VirtualMachineImageImport {
	return &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      imageID,
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

type testSetup struct {
	w  *mock.MockWaiter
	ic *image.Client
}

func defaultTestSetup(objs ...client.Object) *testSetup {
	c := test.FakeClient(objs...)
	l := cfboshlog.New(boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr))
	w := mock.NewMockWaiter(c, projectID)
	ic := &image.Client{
		Client:    c,
		ProjectID: projectID,
		Waiter:    w,
		Logger:    l,
	}

	return &testSetup{
		w:  w,
		ic: ic,
	}
}

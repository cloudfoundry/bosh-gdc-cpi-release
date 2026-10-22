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

package vm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait/mock"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

var (
	ctx       = context.Background()
	projectID = "test-project"
	publicKey = "test-key"
	sshUser   = "ssh-user"
	vmName    = "test-vm"
)

var _ VMARAccessChecker = &VMAccessRequestService{}

func TestCheckVMAccessRequestReady_ExistsAndReady(t *testing.T) {
	vmar := virtualMachineAccessRequest(vmName, publicKey)
	currentTime := time.Now()
	vmar.CreationTimestamp.Time = currentTime.Add(-25 * time.Minute)
	vmar.Status = vmv1.VirtualMachineAccessRequestStatus{
		State: vmv1.VMAccessRequestStateConfigured,
	}
	c := test.FakeClient(vmar)

	testWaiter := mock.NewMockWaiter(c, projectID)
	vmarSrv := NewVMAccessRequestService(c, projectID, sshUser, testWaiter)

	if err := vmarSrv.EnsureVMARExistAndReady(vmName, publicKey); err != nil {
		t.Fatalf("got error: %v", err)
	}

	gotList := list(c, t)
	if len(gotList) != 1 {
		t.Fatalf("got %d VMARs, expected 1", len(gotList))
	}

	if diff := cmp.Diff(vmar, gotList[0], cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name", "ResourceVersion", "CreationTimestamp")); diff != "" {
		t.Fatalf("test failed, vmar diff (-want +got):\n%s", diff)
	}
}

func TestCheckVMAccessRequestReady_ExistsAndExpired(t *testing.T) {
	vmar := virtualMachineAccessRequest(vmName, publicKey)
	currentTime := time.Now()
	vmar.CreationTimestamp.Time = currentTime.Add(+25 * time.Hour)
	vmar.Status = vmv1.VirtualMachineAccessRequestStatus{
		State: vmv1.VMAccessRequestStateFailed,
	}
	c := test.FakeClient(vmar)

	testWaiter := mock.NewMockWaiter(c, projectID)
	vmarSrv := NewVMAccessRequestService(c, projectID, sshUser, testWaiter)

	if err := vmarSrv.EnsureVMARExistAndReady(vmName, publicKey); err != nil {
		t.Fatalf("got error: %v", err)
	}

	gotList := list(c, t)
	for _, got := range gotList {
		if got.CreationTimestamp != vmar.CreationTimestamp {
			// Find newly created one
			return
		}
	}

	t.Fatal("no new VMAR created")
}

func TestCheckVMAccessRequestReady_MultiExistsAndReady(t *testing.T) {
	currentTime := time.Now()
	ts := currentTime.Add(-25 * time.Minute)

	vmar1 := virtualMachineAccessRequest(vmName, publicKey)
	vmar1.CreationTimestamp.Time = ts
	updateVirtualMachineAccessRequest(fmt.Sprintf("%s-1", vmName), vmv1.VMAccessRequestStateFailed, vmar1)
	vmar2 := virtualMachineAccessRequest(vmName, publicKey)
	vmar2.CreationTimestamp.Time = ts
	updateVirtualMachineAccessRequest(fmt.Sprintf("%s-2", vmName), vmv1.VMAccessRequestStatePending, vmar2)
	vmar3 := virtualMachineAccessRequest(vmName, publicKey)
	vmar3.CreationTimestamp.Time = ts
	updateVirtualMachineAccessRequest(fmt.Sprintf("%s-3", vmName), vmv1.VMAccessRequestStateConfigured, vmar3)
	c := test.FakeClient(vmar1, vmar2, vmar3)

	testWaiter := mock.NewMockWaiter(c, projectID)
	vmarSrv := NewVMAccessRequestService(c, projectID, sshUser, testWaiter)

	if err := vmarSrv.EnsureVMARExistAndReady(vmName, publicKey); err != nil {
		t.Fatalf("got error: %v", err)
	}

	gotList := list(c, t)
	if len(gotList) != 3 {
		t.Fatalf("got %d VMARs, expected 3", len(gotList))
	}
}

func TestCheckVMAccessRequestReady_NotExists(t *testing.T) {
	otherVMName := fmt.Sprintf("%s-suffix", vmName)
	otherVMAR := &vmv1.VirtualMachineAccessRequest{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", otherVMName),
			Namespace:    projectID,
		},
		Spec: vmv1.VirtualMachineAccessRequestSpec{
			SSH: vmv1.SSHSpec{
				Key: publicKey,
				TTL: metav1.Duration{
					Duration: 24 * time.Hour,
				},
			},
			User: sshUser,
			VM:   otherVMName,
		},
	}

	c := test.FakeClient(otherVMAR)

	testWaiter := mock.NewMockWaiter(c, projectID)
	vmarSrv := NewVMAccessRequestService(c, projectID, sshUser, testWaiter)

	if err := vmarSrv.EnsureVMARExistAndReady(vmName, publicKey); err != nil {
		t.Fatalf("got error: %v", err)
	}

	gotList := list(c, t)
	if len(gotList) == 0 {
		t.Fatal("vmar is not created")
	}

	if len(gotList) != 1 {
		t.Fatal("unexpected vmar is created")
	}

	expected := vmarSrv.virtualMachineAccessRequest(vmName, publicKey)
	if diff := cmp.Diff(expected, gotList[0], cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name", "ResourceVersion")); diff != "" {
		t.Fatalf("test failed, vmar diff (-want +got):\n%s", diff)
	}
}

func TestCheckVMAccessRequestReady_ExistsAndFailed(t *testing.T) {
	vmar := virtualMachineAccessRequest(vmName, publicKey)
	currentTime := time.Now()
	vmar.CreationTimestamp.Time = currentTime.Add(-25 * time.Minute)
	vmar.Status = vmv1.VirtualMachineAccessRequestStatus{
		State: vmv1.VMAccessRequestStateFailed,
	}
	c := test.FakeClient(vmar)

	testWaiter := mock.NewMockWaiter(c, projectID)
	vmarSrv := NewVMAccessRequestService(c, projectID, sshUser, testWaiter)

	if err := vmarSrv.EnsureVMARExistAndReady(vmName, publicKey); err != nil {
		t.Fatalf("got error: %v", err)
	}

	gotList := list(c, t)
	for _, got := range gotList {
		if got.CreationTimestamp != vmar.CreationTimestamp {
			// Find newly created one
			return
		}
	}

	t.Fatalf("no new VMAR created")
}

func list(c client.Client, t *testing.T) []*vmv1.VirtualMachineAccessRequest {
	vmarList := &vmv1.VirtualMachineAccessRequestList{}
	if err := c.List(ctx, vmarList, &client.ListOptions{
		Namespace: projectID}); err != nil {
		t.Errorf("list access request for VM %s: %v", vmName, err)
	}

	var gotList []*vmv1.VirtualMachineAccessRequest
	for i, vmar := range vmarList.Items {
		if vmar.Spec.VM == vmName {
			gotList = append(gotList, &vmarList.Items[i])
		}
	}
	return gotList
}

func updateVirtualMachineAccessRequest(name string, state vmv1.VirtualMachineAccessRequestState, vmar *vmv1.VirtualMachineAccessRequest) {
	vmar.Name = name
	vmar.Status.State = state
}

func virtualMachineAccessRequest(vmName string, publicKey string) *vmv1.VirtualMachineAccessRequest {
	return &vmv1.VirtualMachineAccessRequest{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", vmName),
			Namespace:    projectID,
		},
		Spec: vmv1.VirtualMachineAccessRequestSpec{
			SSH: vmv1.SSHSpec{
				Key: publicKey,
				TTL: metav1.Duration{
					Duration: 24 * time.Hour,
				},
			},
			User: sshUser,
			VM:   vmName,
		},
	}
}

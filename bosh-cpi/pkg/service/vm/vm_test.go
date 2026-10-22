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
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	"k8s.io/apimachinery/pkg/api/resource"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait/mock"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

func TestStartVM(t *testing.T) {
	vmID := "test-vm"

	testCases := map[string]struct {
		vm *vmv1.VirtualMachine
	}{
		"Running VM": {
			vm: virtualMachine(vmID, withState(vmv1.VirtualMachineStateRunning)),
		},
		"Stopped VM": {
			vm: virtualMachine(vmID, withState(vmv1.VirtualMachineStateStopped)),
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			ts := newDefaultTestSetup(tc.vm)
			ts.w.Err = errors.New("wait error")

			// server side action updates VM to `Running` state with `GuestEnvironmentSynced` condition type status set to true.
			action := func(ctx context.Context, c client.Client) error {
				key := client.ObjectKey{
					Name:      vmID,
					Namespace: projectID,
				}
				vm := &vmv1.VirtualMachine{}
				if err := c.Get(ctx, key, vm); err != nil {
					return fmt.Errorf("get VM: %v", err)
				}

				vm.Status.State = vmv1.VirtualMachineStateRunning
				vm.Status.Conditions = []metav1.Condition{
					{
						Type:    vmv1.ConditionTypeGuestEnvironmentSynced,
						Status:  metav1.ConditionTrue,
						Reason:  vmv1.ReasonAccessManagementReady,
						Message: "VM guest env is synced",
					},
				}

				if err := c.Update(ctx, vm); err != nil {
					return fmt.Errorf("update VM: %v", err)
				}
				return nil
			}

			ts.w.ServerSideActions["VirtualMachine"] = action
			ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

			if err := ts.v.Start(ctx, tc.vm); err != nil {
				t.Fatalf("test get un expected err : %v", err)
			}

			got, err := ts.f.FindVM(ctx, vmID)
			if err != nil {
				t.Fatal("test failed, cannot find vm for verification")
			}

			expectedRunningState := vmv1.VirtualMachineRunningStateRunning
			if *got.Spec.RunningState != expectedRunningState {
				t.Fatalf("test failed, got vm spec runningState %s, expected %s", *got.Spec.RunningState, expectedRunningState)
			}
		})
	}
}

func TestStopVM(t *testing.T) {
	vmID := "test-vm"

	testCases := map[string]struct {
		vm *vmv1.VirtualMachine
	}{
		"Running VM": {
			vm: virtualMachine(vmID, withState(vmv1.VirtualMachineStateRunning)),
		},
		"Stopped VM": {
			vm: virtualMachine(vmID, withState(vmv1.VirtualMachineStateStopped)),
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			ts := newDefaultTestSetup(tc.vm)
			ts.w.Err = errors.New("wait error")

			// server side action updates VM to `Stopped` state with `Editable` condition type status set to true.
			action := func(ctx context.Context, c client.Client) error {
				key := client.ObjectKey{
					Name:      vmID,
					Namespace: projectID,
				}
				vm := &vmv1.VirtualMachine{}
				if err := c.Get(ctx, key, vm); err != nil {
					return fmt.Errorf("get VM: %v", err)
				}

				vm.Status.State = vmv1.VirtualMachineStateStopped
				vm.Status.Conditions = []metav1.Condition{
					{
						Type:    vmv1.ConditionTypeEditable,
						Status:  metav1.ConditionTrue,
						Reason:  vmv1.ReasonVirtualMachineStopped,
						Message: "VM is stopped",
					},
				}

				if err := c.Update(ctx, vm); err != nil {
					return fmt.Errorf("update VM: %v", err)
				}
				return nil
			}

			ts.w.ServerSideActions["VirtualMachine"] = action
			ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

			if err := ts.v.Stop(ctx, tc.vm); err != nil {
				t.Fatalf("test get un expected err : %v", err)
			}

			got, err := ts.f.FindVM(ctx, vmID)
			if err != nil {
				t.Fatal("test failed, cannot find vm for verification")
			}

			expectedRunningState := vmv1.VirtualMachineRunningStateStopped
			if *got.Spec.RunningState != expectedRunningState {
				t.Fatalf("test failed, got vm spec runningState %s, expected %s", *got.Spec.RunningState, expectedRunningState)
			}
		})
	}
}

func TestWaitSuccess(t *testing.T) {
	vmID := "test-vm"

	targetState := vmv1.VirtualMachineStateRunning
	vm := virtualMachine(vmID, withState(targetState))

	ts := newDefaultTestSetup(vm)

	if err := ts.v.WaitForVMState(ctx, vm.Name, targetState, vmv1.ConditionTypeGuestEnvironmentSynced, metav1.ConditionTrue, 2*time.Second); err != nil {
		t.Fatalf("test get un expected err : %v", err)
	}

	got, err := ts.f.FindVM(ctx, vmID)
	if err != nil {
		t.Fatal("test failed, cannot find vm for verification")
	}

	gotState := got.Status.State
	if gotState != targetState {
		t.Fatalf("test failed, got vm state %s, expected %s", gotState, targetState)
	}
}

func TestWaitFailed(t *testing.T) {
	vmID := "test-vm"

	initState := vmv1.VirtualMachineStateStopped
	targetState := vmv1.VirtualMachineStateRunning

	vm := virtualMachine(vmID, withState(initState))
	ts := newDefaultTestSetup(vm)
	ts.w.Err = errors.New("wait error")
	expectedError := fmt.Sprintf("wait until VM %s state is \"Running\" and condition type \"GuestEnvironmentSynced\" is \"True\": %s", vmID, ts.w.Err.Error())

	if err := ts.v.WaitForVMState(ctx, vm.Name, targetState, vmv1.ConditionTypeGuestEnvironmentSynced, metav1.ConditionTrue, 2*time.Second); err == nil {
		t.Fatal("test get nil err, expected err")
	} else if !strings.Contains(err.Error(), expectedError) {
		t.Fatalf("got error message %q, expected %q", err, expectedError)
	}

	got, err := ts.f.FindVM(ctx, vmID)
	if err != nil {
		t.Fatal("test failed, cannot find vm for verification")
	}

	gotState := got.Status.State
	if gotState == targetState && meta.IsStatusConditionTrue(got.Status.Conditions, vmv1.ConditionTypeGuestEnvironmentSynced) {
		t.Fatalf("test failed, got unexpected vm state %s, expected %s", targetState, initState)
	}
}

func TestWaitTimeout(t *testing.T) {
	vmID := "test-vm"
	initState := vmv1.VirtualMachineStateStopped
	targetState := vmv1.VirtualMachineStateRunning
	vm := virtualMachine(vmID, withState(initState))
	ts := newDefaultTestSetup(vm)

	testCases := map[string]struct {
		status         vmv1.VirtualMachineStatus
		expectedSubErr string
	}{
		"vm provision failed": {
			status: vmv1.VirtualMachineStatus{
				State:  vmv1.VirtualMachineStateProvisioning,
				Reason: vmv1.VirtualMachineProvisioningFailed,
			},
			expectedSubErr: "Latest status:\n State: \"Provisioning\"\n Reason: \"VirtualMachineProvisioningFailed\"",
		},
		"vm in error configuration with no ip allocation": {
			status: vmv1.VirtualMachineStatus{
				State:   vmv1.VirtualMachineStateErrorConfiguration,
				Reason:  vmv1.InterfaceCreationFailed,
				Message: "no IP",
			},
			expectedSubErr: "Latest status:\n State: \"ErrorConfiguration\"\n Reason: \"InterfaceCreationFailed\"\n Message: \"no IP\"",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			// server side action
			action := func(ctx context.Context, c client.Client) error {
				key := client.ObjectKey{
					Name:      vmID,
					Namespace: projectID,
				}
				vm := &vmv1.VirtualMachine{}
				if err := c.Get(ctx, key, vm); err != nil {
					return fmt.Errorf("get VM: %v", err)
				}

				vm.Status.State = tc.status.State
				vm.Status.Reason = tc.status.Reason
				vm.Status.Message = tc.status.Message

				if err := c.Update(ctx, vm); err != nil {
					return fmt.Errorf("update VM: %v", err)
				}
				return nil
			}

			ts.w.ServerSideActions["VirtualMachine"] = action
			ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

			if err := ts.v.WaitForVMState(ctx, vm.Name, targetState, vmv1.ConditionTypeGuestEnvironmentSynced, metav1.ConditionTrue, 2*time.Second); err == nil {
				t.Fatal("test get nil err, expected err")
			} else if !strings.Contains(err.Error(), tc.expectedSubErr) {
				t.Fatalf("got error message %q, expected error contains %q", err, tc.expectedSubErr)
			}

			got, err := ts.f.FindVM(ctx, vmID)
			if err != nil {
				t.Fatal("test failed, cannot find vm for verification")
			}

			gotState := got.Status.State
			if gotState == targetState && meta.IsStatusConditionTrue(got.Status.Conditions, vmv1.ConditionTypeGuestEnvironmentSynced) {
				t.Fatalf("test failed, got unexpected vm state %s, expected %s", targetState, initState)
			}
		})
	}
}

func TestWaitTerminated(t *testing.T) {
	vmID := "test-vm"
	initState := vmv1.VirtualMachineStateStopped
	targetState := vmv1.VirtualMachineStateRunning
	vm := virtualMachine(vmID, withState(initState))
	ts := newDefaultTestSetup(vm)
	expectedSubErr := fmt.Sprintf("VM %s was unschedulable.\nLatest status:\n State: \"ErrorUnschedulable\"", vmID)

	// server side action on VM
	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmID,
			Namespace: projectID,
		}
		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateUnschedulable

		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}

	ts.w.ServerSideActions["VirtualMachine"] = action
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	if err := ts.v.WaitForVMState(ctx, vm.Name, targetState, vmv1.ConditionTypeGuestEnvironmentSynced, metav1.ConditionTrue, 2*time.Second); err == nil {
		t.Fatal("test get nil err, expected err")
	} else if !strings.Contains(err.Error(), expectedSubErr) {
		t.Fatalf("got error message %q, expected error contains %q", err, expectedSubErr)
	}

	got, err := ts.f.FindVM(ctx, vmID)
	if err != nil {
		t.Fatal("test failed, cannot find vm for verification")
	}

	gotState := got.Status.State
	if gotState == targetState && meta.IsStatusConditionTrue(got.Status.Conditions, vmv1.ConditionTypeGuestEnvironmentSynced) {
		t.Fatalf("test failed, got unexpected vm state %s, expected %s", targetState, initState)
	}
}

func TestDeleteDiskSuccess_DiskExists(t *testing.T) {
	diskName := "test-disk"
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diskName,
			Namespace: projectID,
		},
	}
	ts := newDefaultTestSetup(disk)

	if err := ts.v.DeleteDisk(ctx, diskName); err != nil {
		t.Fatalf("get error: %v", err)
	}
}

func TestDeleteDiskSuccess_DiskNotExists(t *testing.T) {
	ts := newDefaultTestSetup()

	if err := ts.v.DeleteDisk(ctx, "foo"); err != nil {
		t.Fatalf("get error: %v", err)
	}
}

func TestDelete(t *testing.T) {
	testCases := map[string]struct {
		initialObjects []client.Object
		vmName         string
		disksToDelete  []string
		disksToKeep    []string
	}{
		"delete a VM": {
			initialObjects: []client.Object{
				virtualMachine("vm-1"),
			},
			vmName: "vm-1",
		},
		"delete a non-existing VM": {
			initialObjects: []client.Object{
				virtualMachine("vm-1"),
			},
			vmName: "vm-2",
		},
		"delete a VM and associated disks with AutoDelete turned on": {
			vmName: "vm-3",
			initialObjects: []client.Object{
				virtualMachine("vm-3",
					withDisks(
						[]vmv1.DiskAttachment{
							{
								Boot:                  ptr.To(true),
								VirtualMachineDiskRef: corev1.LocalObjectReference{Name: "disk1"},
								AutoDelete:            ptr.To(true),
							},
							{
								Boot:                  ptr.To(false),
								VirtualMachineDiskRef: corev1.LocalObjectReference{Name: "disk2"},
								AutoDelete:            ptr.To(false),
							},
						},
					),
				),
				virtualMachineDisk("disk1"),
				virtualMachineDisk("disk2"),
			},
			disksToDelete: []string{"disk1"},
			disksToKeep:   []string{"disk2"},
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			ctx := context.Background()
			ts := newDefaultTestSetup(tc.initialObjects...)

			if err := ts.v.Delete(ctx, tc.vmName); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if _, err := ts.f.FindVM(ctx, tc.vmName); err == nil ||
				!apierrors.IsNotFound(err) {
				t.Fatalf("unexpected error: want 'NotFound' but got %v", err)
			}

			for _, disk := range tc.disksToDelete {
				if _, err := ts.f.FindDisk(ctx, disk); err == nil || !apierrors.IsNotFound(err) {
					t.Fatalf("unexpected error: want 'NotFound' but got %v", err)
				}
			}

			for _, disk := range tc.disksToKeep {
				if _, err := ts.f.FindDisk(ctx, disk); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestDeleteError(t *testing.T) {
	c := test.FakeInvalidClient()
	v := &VMService{Client: c}
	if err := v.Delete(context.Background(), "vm-name"); err == nil {
		t.Error("expecting an error but got nil")
	}
}

type testSetup struct {
	c client.Client
	f *service.CPIFinder
	v *VMService
	w *mock.MockWaiter
}

func newDefaultTestSetup(objs ...client.Object) *testSetup {
	c := test.FakeClient(objs...)
	w := mock.NewMockWaiter(c, projectID)
	f := service.NewCPIFinder(c, projectID)
	logger := cfboshlog.New(boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr))
	v := &VMService{
		Client:    c,
		ProjectID: projectID,
		Waiter:    w,
		Logger:    logger,
		Timeout:   &test.Timeout,
	}
	return &testSetup{
		c: c,
		f: f,
		v: v,
		w: w,
	}
}

type option func(*vmv1.VirtualMachine)

func virtualMachine(name string, opts ...option) *vmv1.VirtualMachine {
	vm := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: projectID,
		},
	}
	for _, apply := range opts {
		apply(vm)
	}
	return vm
}

func virtualMachineDisk(name string) *vmv1.VirtualMachineDisk {
	return &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: projectID,
		},
		Spec: vmv1.VirtualMachineDiskSpec{
			Size: resource.MustParse("10G"),
		},
	}
}

func withState(state vmv1.VirtualMachineState) option {
	return func(vm *vmv1.VirtualMachine) {
		vm.Status.State = state
	}
}

func withDisks(disks []vmv1.DiskAttachment) option {
	return func(vm *vmv1.VirtualMachine) {
		vm.Spec.Disks = disks
	}
}

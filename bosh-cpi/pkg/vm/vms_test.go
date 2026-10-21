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
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"

	consts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/agent"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait/mock"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/unit"
)

var (
	ctx                 = context.Background()
	logger              = cfboshlog.New(boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr))
	uuidGen             = uuidGeneratorMock{}
	agentOpt            = apiv1.AgentOptions{}
	externalAccessPorts = []config.ServerPort{
		{
			Name:     "ssh",
			Protocol: "TCP",
			Port:     22,
		},
	}

	sshManager = &mockSSH{}
	aes        = &agent.AgentEnvService{SSHManager: sshManager}

	mockSuccessMetadataClient = &mockMetadataClient{}

	projectID = "test-project"

	mockVMUUID         = "7ce97a29-218d-4025-751c-0aa75406f103"
	mockExpectedVMName = "vm-7ce97a29"
	mockIngressIP      = "1.2.3.4"
	conditionTypes     = []metav1.Condition{
		{
			Type:    vmv1.ConditionTypeGuestEnvironmentSynced,
			Status:  metav1.ConditionTrue,
			Reason:  vmv1.ReasonAccessManagementReady,
			Message: "VM guest env is synced",
		},
	}
	vmNodeNetworkLancerInterfaceName = "net1"
)

func TestHasVM(t *testing.T) {
	vm := virtualMachine("foo")
	ts := newDefaultTestSetup(vm)

	testCases := map[string]struct {
		expected bool
		vmID     string
	}{
		"has VM": {
			expected: true,
			vmID:     "foo",
		},
		"not has VM": {
			expected: false,
			vmID:     "bar",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			actual, err := ts.v.HasVM(apiv1.NewVMCID(tc.vmID))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if actual != tc.expected {
				t.Fatalf("HasVM(%s) = %t, but want %t", tc.vmID, actual, tc.expected)
			}
		})
	}
}

func TestHasVMError(t *testing.T) {
	fakeFinder := &errorObjectFinder{}
	fc := test.FakeClient()

	vms := NewVMs(fc, projectID, uuidGen, agentOpt, externalAccessPorts, logger, fakeFinder, aes, &mockImageService{},
		&wait.Waiter{}, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)
	if _, err := vms.HasVM(apiv1.NewVMCID("foo")); err == nil {
		t.Error("got nil but expect an error")
	}
}

func TestSetVMMetadataSuccess(t *testing.T) {
	vmID := "target-vm"
	longStr := "abvxcnbhteafuximxetbxvuyddcghlnqmkwlvebchkfuyyuybbaepbzgpdcposlnz"

	testCases := map[string]struct {
		vm       *vmv1.VirtualMachine
		metadata map[string]interface{}
		expected map[string]string
	}{
		"SetMetadata on VM without labels": {
			vm: virtualMachine(
				vmID,
				withLabels(nil),
			),
			metadata: map[string]interface{}{
				"key1": "value1",
				"key2": "value2",
			},
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		"SetMetadata on VM with labels": {
			vm: virtualMachine(vmID, withLabels(map[string]string{
				"key1": "value1",
			})),
			metadata: map[string]interface{}{
				"key2": "value2",
			},
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		"SetMetadata on VM with labels to overwrite": {
			vm: virtualMachine(vmID, withLabels(map[string]string{
				"key1": "value1",
				"key2": "value2",
			})),
			metadata: map[string]interface{}{
				"key2": "new-value",
			},
			expected: map[string]string{
				"key1": "value1",
				"key2": "new-value",
			},
		},
		"SetMetadata on VM with the same labels": {
			vm: virtualMachine(vmID, withLabels(map[string]string{
				"key1": "value1",
				"key2": "value2",
			})),
			metadata: map[string]interface{}{
				"key1": "value1",
				"key2": "value2",
			},
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		"SetMetadata patches invalid characters": {
			vm: virtualMachine(vmID, withLabels(nil)),
			metadata: map[string]interface{}{
				"created_at": "2024-01-04T16:03:38Z",
				"name":       "bosh/0",
			},
			expected: map[string]string{
				"created_at": "20240104T160338Z",
				"name":       "bosh--0",
			},
		},
		"SetMetadata trims values with length violation": {
			vm: virtualMachine(vmID, withLabels(nil)),
			metadata: map[string]interface{}{
				"instance_name": longStr,
			},
			expected: map[string]string{
				"instance_name": longStr[0:63],
			},
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			ts := newDefaultTestSetup(tc.vm)
			if err := ts.v.SetVMMetadata(apiv1.NewVMCID(vmID), apiv1.NewVMMeta(tc.metadata)); err != nil {
				t.Fatalf("test failed, get unexpected error: %v", err)
			}
			updatedVM, err := ts.f.FindVM(context.Background(), vmID)
			if err != nil {
				t.Fatalf("failed to find VM: %v", err)
			}

			if diff := cmp.Diff(tc.expected, updatedVM.ObjectMeta.Labels); diff != "" {
				t.Fatalf("test requests diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetVMMetadataSuccessWithSetInAnnotations(t *testing.T) {
	vmID := "target-vm"
	longStr := "abvxcnbhteafuximxetbxvuyddcghlnqmkwlvebchkfuyyuybbaepbzgpdcposlnz"
	expectedLongStr := longStr[0:63]

	testCases := map[string]struct {
		vm                  *vmv1.VirtualMachine
		metadata            map[string]interface{}
		expectedLables      map[string]string
		expectedAnnotations map[string]string
	}{
		"SetMetadata on VM without annotations - no length violation": {
			vm: virtualMachine(
				vmID,
				withAnnotations(nil),
			),
			metadata: map[string]interface{}{
				"key1": "value1",
				"key2": "value2",
			},
			expectedLables: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
			expectedAnnotations: nil,
		},
		"SetMetadata on VM with annotations - no length vilation": {
			vm: virtualMachine(vmID, withAnnotations(map[string]string{
				"anno_key1": "value1",
			})),
			metadata: map[string]interface{}{
				"key2": "value2",
			},
			expectedLables: map[string]string{
				"key2": "value2",
			},
			expectedAnnotations: map[string]string{
				"anno_key1": "value1",
			},
		},
		"SetMetadata on VM with annotations - with length violation": {
			vm: virtualMachine(vmID, withAnnotations(map[string]string{
				"anno_key1": "value1",
				"anno_key2": "value2",
			})),
			metadata: map[string]interface{}{
				"key1": longStr,
			},
			expectedLables: map[string]string{
				"key1": expectedLongStr,
			},
			expectedAnnotations: map[string]string{
				"anno_key1": "value1",
				"anno_key2": "value2",
				"key1":      longStr,
			},
		},
		"SetMetadata on VM with the same annotations - with length violation": {
			vm: virtualMachine(vmID, withAnnotations(map[string]string{
				"anno_key1": "value1",
				"anno_key2": "value2",
				"key1":      longStr,
			})),
			metadata: map[string]interface{}{
				"key1": longStr,
			},
			expectedLables: map[string]string{
				"key1": expectedLongStr,
			},
			expectedAnnotations: map[string]string{
				"anno_key1": "value1",
				"anno_key2": "value2",
				"key1":      longStr,
			},
		},
		"SetMetadata on VM with annotations - overwrite with length violation": {
			vm: virtualMachine(vmID, withAnnotations(map[string]string{
				"anno_key1": "value1",
				"anno_key2": "value2",
				"key1":      "value1",
			})),
			metadata: map[string]interface{}{
				"key1": longStr,
			},
			expectedLables: map[string]string{
				"key1": expectedLongStr,
			},
			expectedAnnotations: map[string]string{
				"anno_key1": "value1",
				"anno_key2": "value2",
				"key1":      longStr,
			},
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			ts := newDefaultTestSetup(tc.vm)
			if err := ts.v.SetVMMetadata(apiv1.NewVMCID(vmID), apiv1.NewVMMeta(tc.metadata)); err != nil {
				t.Fatalf("test failed, get unexpected error: %v", err)
			}
			updatedVM, err := ts.f.FindVM(context.Background(), vmID)
			if err != nil {
				t.Fatalf("failed to find VM: %v", err)
			}

			if diff := cmp.Diff(tc.expectedLables, updatedVM.ObjectMeta.Labels); diff != "" {
				t.Fatalf("test labels diff (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.expectedAnnotations, updatedVM.ObjectMeta.Annotations); diff != "" {
				t.Fatalf("test annotations diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetVMMetadataFailed(t *testing.T) {
	oldWait := metadataRetryWait
	metadataRetryWait = wait.Waiting{
		Wait:  2 * time.Second,
		Every: 1 * time.Second,
	}
	defer func() { metadataRetryWait = oldWait }()

	ef := &errorObjectFinder{}
	fc := test.FakeClient()
	vms := NewVMs(fc, projectID, uuidGen, agentOpt, externalAccessPorts, logger, ef, aes, &mockImageService{},
		&wait.Waiter{Logger: logger}, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)
	if err := vms.SetVMMetadata(apiv1.NewVMCID(test.WantedObjName), apiv1.NewVMMeta(nil)); err == nil {
		t.Fatal("got nil but expect an error")
	}
}

func TestSetVMMetadataSucceedAfterRetry(t *testing.T) {
	oldWait := metadataRetryWait
	metadataRetryWait = wait.Waiting{
		Wait:  2 * time.Second,
		Every: 1 * time.Second,
	}
	defer func() { metadataRetryWait = oldWait }()

	vmID := "test-vm"
	vm := virtualMachine(vmID, withLabels(nil))

	fc := test.FakeClient(vm)
	f := service.NewCPIFinder(fc, projectID)
	rf := &retryObjectFinder{CPIFinder: f}

	vms := NewVMs(fc, projectID, uuidGen, agentOpt, externalAccessPorts, logger, rf, aes, &mockImageService{},
		&wait.Waiter{Logger: logger}, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)

	metadata := map[string]interface{}{"key1": "value1"}
	if err := vms.SetVMMetadata(apiv1.NewVMCID(vmID), apiv1.NewVMMeta(metadata)); err != nil {
		t.Fatalf("get unexpected error: %v", err)
	}

	if rf.calls != 2 {
		t.Fatalf("got %d calls, expected 2", rf.calls)
	}

	updatedVM, err := f.FindVM(context.Background(), vmID)
	if err != nil {
		t.Fatalf("find VM: %v", err)
	}

	expected := map[string]string{"key1": "value1"}
	if diff := cmp.Diff(expected, updatedVM.ObjectMeta.Labels); diff != "" {
		t.Fatalf("requests diff (-want +got):\n%s", diff)
	}
}

// Tests for DeleteVM
func TestDeleteVMSuccess(t *testing.T) {
	disk := getDefaultDisk()

	// Fake client cannot auto delete VM attached disks with autoDelete true.
	// Set disk autoDelete false for success test to avoid DeleteVM verification for autoDelete disk
	vm := getDefaultVM(disk.Name, false /* diskAutoDelete */)

	ts := newDefaultTestSetup(disk, vm)

	if err := ts.v.DeleteVM(apiv1.NewVMCID(vm.Name)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify vm deletion
	hasVM, err := ts.f.HasVM(ctx, vm.Name)
	if err != nil {
		t.Fatalf("unexpected error while retrieving vm to verify deletion: %v", err)
	}
	if hasVM {
		t.Fatal("get unexpected vm")
	}

	// Verify non-autoDelete disk exists
	hasDisk, err := ts.f.HasDisk(ctx, disk.Name)
	if err != nil {
		t.Fatalf("test failed, get unexpected error while retrieving disk %s to verify deletion: %v", disk.Name, err)
	}
	if !hasDisk {
		t.Fatalf("test failed, disk %s was deleted unexpectly", disk.Name)
	}
}

func TestDeleteVMFailed(t *testing.T) {
	c := test.FakeInvalidClient()
	ts := newTestSetup(c)
	if err := ts.v.DeleteVM(apiv1.NewVMCID("vm-id")); err == nil {
		t.Fatal("got nil but expect an error")
	}
}

// Tests of RebootVM
func TestRebootVMSuccess(t *testing.T) {
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

			if err := ts.v.RebootVM(apiv1.NewVMCID(vmID)); err != nil {
				t.Fatalf("test get un expected err : %v", err)
			}

			got, err := ts.f.FindVM(ctx, vmID)
			if err != nil {
				t.Fatal("test failed, cannot find vm for verification")
			}

			if *got.Spec.RunningState != vmv1.VirtualMachineRunningStateRunning {
				t.Fatalf("test failed, got vm spec runningState %s, expected %s", *got.Spec.RunningState, vmv1.VirtualMachineRunningStateRunning)
			}
		})
	}
}

func TestRebootVMInvalidStatus(t *testing.T) {
	vm := virtualMachine("foo")

	testStates := []vmv1.VirtualMachineState{
		vmv1.VirtualMachineStateCrashLoopBackoff,
		vmv1.VirtualMachineStateDiskError,
		vmv1.VirtualMachineStateDiskPending,
		vmv1.VirtualMachineStateErrorConfiguration,
		vmv1.VirtualMachineStateMigrating,
		vmv1.VirtualMachineStatePaused,
		vmv1.VirtualMachineStatePending,
		vmv1.VirtualMachineStatePendingIPAllocation,
		vmv1.VirtualMachineStateProvisioning,
		vmv1.VirtualMachineStateStarting,
		vmv1.VirtualMachineStateTerminating,
		vmv1.VirtualMachineStateUnknown,
		vmv1.VirtualMachineStateUnschedulable,
	}

	for _, state := range testStates {
		vm.Status.State = state

		ts := newDefaultTestSetup(vm)
		if err := ts.v.RebootVM(apiv1.NewVMCID("foo")); err == nil {
			t.Fatalf("test failed, expected to get error for vm with status %s", state)
		}
	}
}

func TestDiskWait_Timeout(t *testing.T) {
	disk := virtualMachineDisk("disk", withDiskState(vmv1.DiskPhaseUnknown))
	ts := newDefaultTestSetup(disk)

	// set server side action to update disk status from "Unknown" to "Pending"
	statePostServerSideAction := vmv1.DiskPhasePending
	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      disk.Name,
			Namespace: projectID,
		}
		if err := c.Get(ctx, key, disk); err != nil {
			return fmt.Errorf("get disk: %v", err)
		}

		disk.Status.Phase = statePostServerSideAction
		if err := c.Update(ctx, disk); err != nil {
			return fmt.Errorf("update disk: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineDisk"] = action
	ts.w.TargetObjects["VirtualMachineDisk"] = &vmv1.VirtualMachineDisk{}

	if err := ts.v.waitForDiskReady(ctx, disk.Name, 2*time.Second); err == nil {
		t.Fatal("got nil, expected err")
	}

	got, err := ts.f.FindDisk(ctx, disk.Name)
	if err != nil {
		t.Fatal("cannot find disk for verification")
	}

	gotState := got.Status.Phase
	if gotState != statePostServerSideAction {
		t.Fatalf("got unexpected disk state %s, expected %s", gotState, statePostServerSideAction)
	}
}

// TestCalculateVMCloudProperties
func TestCalculateVMCloudProperties(t *testing.T) {
	testCases := map[string]struct {
		input       apiv1.VMResources
		result      apiv1.VMCloudPropsImpl
		initVMTypes []client.Object
	}{
		"Found vmType with matched resource memory in GiB": {
			input: apiv1.VMResources{
				CPU:               2,
				RAM:               2 * unit.KiB,
				EphemeralDiskSize: 1 * unit.KiB,
			},
			initVMTypes: []client.Object{
				virtualMachineType(2, "2Gi"),
			},
			result: apiv1.NewVMCloudPropsFromMap(map[string]interface{}{
				"virtualMachineType":  "test-cpu-2",
				"bootDiskSizeGB":      1,
				"ephemeralDiskSizeGB": 1,
			}),
		},
		"Found vmType with matched resource and memory in GB": {
			input: apiv1.VMResources{
				CPU:               2,
				RAM:               2 * unit.KiB,
				EphemeralDiskSize: 1 * unit.KiB,
			},
			initVMTypes: []client.Object{
				virtualMachineType(2, "2G"),
			},
			result: apiv1.NewVMCloudPropsFromMap(map[string]interface{}{
				"virtualMachineType":  "test-cpu-2",
				"bootDiskSizeGB":      1,
				"ephemeralDiskSizeGB": 1,
			}),
		},
		"Found vmType with matched resource and fixed disk size": {
			input: apiv1.VMResources{
				CPU:               2,
				RAM:               2 * unit.KiB,
				EphemeralDiskSize: 1*unit.KiB + 256,
			},
			initVMTypes: []client.Object{
				virtualMachineType(2, "2G"),
			},
			result: apiv1.NewVMCloudPropsFromMap(map[string]interface{}{
				"virtualMachineType":  "test-cpu-2",
				"bootDiskSizeGB":      2,
				"ephemeralDiskSizeGB": 2,
			}),
		},
		"find vmType with matched cpu with nearest larget memory": {
			input: apiv1.VMResources{
				CPU:               2,
				RAM:               30 * unit.KiB,
				EphemeralDiskSize: 10*unit.KiB + 256,
			},
			initVMTypes: []client.Object{
				virtualMachineType(2, "10G"),
				virtualMachineType(2, "20G"),
				virtualMachineType(4, "30G"),
			},
			result: apiv1.NewVMCloudPropsFromMap(map[string]interface{}{
				"virtualMachineType":  vmTypeName(4, "30G"),
				"bootDiskSizeGB":      11,
				"ephemeralDiskSizeGB": 11,
			}),
		},
		"find vmType with matched cpu with nearest larget memory in GiB": {
			input: apiv1.VMResources{
				CPU:               2,
				RAM:               30 * unit.KiB,
				EphemeralDiskSize: 10*unit.KiB + 256,
			},
			initVMTypes: []client.Object{
				virtualMachineType(2, "10Gi"),
				virtualMachineType(4, "40Gi"),
				virtualMachineType(2, "40Gi"),
			},
			result: apiv1.NewVMCloudPropsFromMap(map[string]interface{}{
				"virtualMachineType":  vmTypeName(2, "40Gi"),
				"bootDiskSizeGB":      11,
				"ephemeralDiskSizeGB": 11,
			}),
		},
		"find vmType with nearest larger cpu and memory": {
			input: apiv1.VMResources{
				CPU:               11,
				RAM:               110 * unit.KiB,
				EphemeralDiskSize: 100*unit.KiB + 512,
			},
			initVMTypes: []client.Object{
				virtualMachineType(9, "50G"),
				virtualMachineType(10, "80G"),
				virtualMachineType(12, "120G"),
				virtualMachineType(12, "200G"),
			},
			result: apiv1.NewVMCloudPropsFromMap(map[string]interface{}{
				"virtualMachineType":  vmTypeName(12, "120G"),
				"bootDiskSizeGB":      101,
				"ephemeralDiskSizeGB": 101,
			}),
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			ts := newDefaultTestSetup(tc.initVMTypes...)

			result, err := ts.v.CalculateVMCloudProperties(tc.input)
			if err != nil {
				t.Fatalf("failed to run test: %v", err)
			}

			expectedJson, err := tc.result.MarshalJSON()
			if err != nil {
				t.Fatalf("failed to marchal json for test expected result: %v", err)
			}
			var propImp = result.(apiv1.VMCloudPropsImpl)
			gotJson, err := propImp.MarshalJSON()
			if err != nil {
				t.Fatalf("failed to marchal json for test result: %v", err)
			}
			if diff := cmp.Diff(expectedJson, gotJson); diff != "" {
				t.Fatalf("test requests diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestCalculateVMCloudPropertiesFailed(t *testing.T) {
	input := apiv1.VMResources{
		CPU:               12,
		RAM:               120 * unit.KiB,
		EphemeralDiskSize: 1 * unit.KiB,
	}

	ts := newDefaultTestSetup(
		virtualMachineType(8, "60G"),
		virtualMachineType(10, "80G"),
	)

	if _, err := ts.v.CalculateVMCloudProperties(input); err == nil {
		t.Fatal("test wants error, got nil")
	}
}

func TestPropertyValidation(t *testing.T) {
	testCases := map[string]struct {
		prop    Property
		wantErr bool
	}{
		"VCPUs and MemorySizeDB are not provided all together when VirtualMachineType is empty": {
			prop: Property{
				Name:              "test-vm",
				BootDiskSizeGB:    10,
				VCPUs:             10,
				BootDiskType:      consts.DiskTypeStandard,
				EphemeralDiskType: consts.DiskTypeStandard,
			},
			wantErr: true,
		},
		"VCPUs is negative": {
			prop: Property{
				Name:              "test-vm",
				BootDiskSizeGB:    10,
				VCPUs:             -1,
				MemorySizeGB:      20,
				BootDiskType:      consts.DiskTypeStandard,
				EphemeralDiskType: consts.DiskTypeStandard,
			},
			wantErr: true,
		},
		"MemorySizeGB is negative": {
			prop: Property{
				Name:              "test-vm",
				BootDiskSizeGB:    10,
				VCPUs:             10,
				MemorySizeGB:      -1,
				BootDiskType:      consts.DiskTypeStandard,
				EphemeralDiskType: consts.DiskTypeStandard,
			},
			wantErr: true,
		},
		"VirtualBootDiskSizeGB not provide": {
			prop: Property{
				Name:               "test-vm",
				VirtualMachineType: "test-cpu-2",
				BootDiskType:       consts.DiskTypeStandard,
				EphemeralDiskType:  consts.DiskTypeStandard,
			},
			wantErr: true,
		},
		"VirtualBootDiskSizeGB invalid": {
			prop: Property{
				Name:               "test-vm",
				VirtualMachineType: "test-cpu-2",
				BootDiskSizeGB:     -1,
				BootDiskType:       consts.DiskTypeStandard,
				EphemeralDiskType:  consts.DiskTypeStandard,
			},
			wantErr: true,
		},
		"EphemeralDiskSizeGB invalid": {
			prop: Property{
				Name:                "test-vm",
				VirtualMachineType:  "test-cpu-2",
				EphemeralDiskSizeGB: 9,
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypePerformance,
			},
			wantErr: true,
		},
		"LoadBalancer is specified with empty Selector": {
			prop: Property{
				Name:                "test-vm",
				VirtualMachineType:  "test-cpu-2",
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypeStandard,
				LoadBalancer: LB{
					Name: "some-lb",
				},
			},
			wantErr: true,
		},
		"LoadBalancer is specified with empty Selector Key": {
			prop: Property{
				Name:                "test-vm",
				VirtualMachineType:  "test-cpu-2",
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypeStandard,
				LoadBalancer: LB{
					Name: "some-lb",
					Selector: LBSelector{
						Value: "lb_selector_value",
					},
				},
			},
			wantErr: true,
		},
		"LoadBalancer is specified with empty Selector Value": {
			prop: Property{
				Name:                "test-vm",
				VirtualMachineType:  "test-cpu-2",
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypeStandard,
				LoadBalancer: LB{
					Name: "some-lb",
					Selector: LBSelector{
						Key: "lb_selector_key",
					},
				},
			},
			wantErr: true,
		},
		"CloudNAT is specified with empty Name": {
			prop: Property{
				Name:                "test-vm",
				VirtualMachineType:  "test-cpu-2",
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypeStandard,
				CloudNAT: CloudNAT{
					Selector: CloudNATSelector{
						Key:   "cn_selector_key",
						Value: "cn_selector_value",
					},
				},
			},
			wantErr: true,
		},
		"CloudNAT is specified with empty Selector Key": {
			prop: Property{
				Name:                "test-vm",
				VirtualMachineType:  "test-cpu-2",
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypeStandard,
				CloudNAT: CloudNAT{
					Name: "some-cn",
					Selector: CloudNATSelector{
						Value: "cn_selector_value",
					},
				},
			},
			wantErr: true,
		},
		"CloudNAT is specified with empty Selector Value": {
			prop: Property{
				Name:                "test-vm",
				VirtualMachineType:  "test-cpu-2",
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypeStandard,
				CloudNAT: CloudNAT{
					Name: "some-cn",
					Selector: CloudNATSelector{
						Key: "cn_selector_key",
					},
				},
			},
			wantErr: true,
		},
		"CloudNAT is specified correctly": {
			prop: Property{
				Name:                "test-vm",
				VirtualMachineType:  "test-cpu-2",
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypeStandard,
				CloudNAT: CloudNAT{
					Name: "some-cn",
					Selector: CloudNATSelector{
						Key:   "cn_selector_key",
						Value: "cn_selector_value",
					},
				},
			},
			wantErr: false,
		},
		"Empty boot and ephemeral disk types defaults to Standard": {
			prop: Property{
				Name:                "test-vm",
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				VCPUs:               10,
				MemorySizeGB:        20,
			},
			wantErr: false,
		},
		"Standard boot and Performance ephemeral disk type": {
			prop: Property{
				Name:                "test-vm",
				BootDiskType:        consts.DiskTypeStandard,
				EphemeralDiskType:   consts.DiskTypePerformance,
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				VCPUs:               10,
				MemorySizeGB:        20,
			},
			wantErr: false,
		},
		"Performance disk types": {
			prop: Property{
				Name:                "test-vm",
				BootDiskType:        consts.DiskTypePerformance,
				EphemeralDiskType:   consts.DiskTypePerformance,
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 10,
				VCPUs:               10,
				MemorySizeGB:        20,
			},
			wantErr: false,
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			emptyBootDiskType := len(tc.prop.BootDiskType) == 0
			emptyEphemeralDiskType := len(tc.prop.EphemeralDiskType) == 0
			err := tc.prop.validate()
			if err != nil && !tc.wantErr {
				t.Fatalf("%v: tc.prop.validate()=%v, want: no error", id, err)
			}
			if err == nil && tc.wantErr {
				t.Fatalf("%v: tc.prop.validate()=%v, want: error", id, err)
			}
			if emptyBootDiskType && tc.prop.BootDiskType != consts.DiskTypeStandard {
				t.Fatalf("%v: tc.prop.validate()=%v, want: %v", id, tc.prop.BootDiskType, consts.DiskTypeStandard)
			}
			if emptyEphemeralDiskType && tc.prop.EphemeralDiskType != consts.DiskTypeStandard {
				t.Fatalf("%v: tc.prop.validate()=%v, want: %v", id, tc.prop.EphemeralDiskType, consts.DiskTypeStandard)
			}
		})
	}
}

func TestCreateVM1Failed_MetadataServerUpdate(t *testing.T) {
	testCreateVMFailed_MetadataServerUpdate("v1", t)
}

func TestCreateVM2Failed_MetadataServerUpdate(t *testing.T) {
	testCreateVMFailed_MetadataServerUpdate("v2", t)
}

func testCreateVMFailed_MetadataServerUpdate(version string, t *testing.T) {
	ts := newDefaultTestSetup(virtualMachineDisk("disk-foo"), virtualMachineDisk("disk-bar"))
	ts.m.image = virtualMachineImage("image-foo")
	ts.v.metadataClient = &mockErrorMetadataClient{}
	ts.v.agentEnvService = nil

	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      mockExpectedVMName,
			Namespace: projectID,
		}

		vmea := &vmv1.VirtualMachineExternalAccess{}
		if err := c.Get(ctx, key, vmea); err != nil {
			return fmt.Errorf("get VMEA: %v", err)
		}

		vmea.Status.IngressIP = mockIngressIP
		if err := c.Update(ctx, vmea); err != nil {
			return fmt.Errorf("update VMEA: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineExternalAccess"] = action

	prop := Property{
		Name:                mockExpectedVMName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}

	diskCIDs := []apiv1.DiskCID{apiv1.NewDiskCID("disk-foo"), apiv1.NewDiskCID("disk-bar")}

	if version == "v1" {
		_, err = ts.v.CreateVM(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	} else {
		_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	}

	expectedError := "send agent setting to metadata server:"
	if err == nil {
		t.Fatal("test failed, got nil, expected error")
	}
	if !strings.Contains(err.Error(), expectedError) {
		t.Fatalf("got error '%s', expected error '%s'", err.Error(), expectedError)
	}
}

func TestCreateVM1Failed_SSHUpload(t *testing.T) {
	testCreateVMFailed_SSHUpload("v1", t)
}

func TestCreateVM2Failed_SSHUpload(t *testing.T) {
	testCreateVMFailed_SSHUpload("v2", t)
}

func testCreateVMFailed_SSHUpload(version string, t *testing.T) {
	ts := newDefaultTestSetup(virtualMachineDisk("disk-foo"), virtualMachineDisk("disk-bar"))
	ts.m.image = virtualMachineImage("image-foo")
	ts.v.agentEnvService = &agent.AgentEnvService{SSHManager: &mockErrorSSH{}}
	ts.v.metadataClient = nil
	ts.v.useMetadataServer = false

	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      mockExpectedVMName,
			Namespace: projectID,
		}

		vmea := &vmv1.VirtualMachineExternalAccess{}
		if err := c.Get(ctx, key, vmea); err != nil {
			return fmt.Errorf("get VMEA: %v", err)
		}

		vmea.Status.IngressIP = mockIngressIP
		if err := c.Update(ctx, vmea); err != nil {
			return fmt.Errorf("update VMEA: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineExternalAccess"] = action

	prop := Property{
		Name:                mockExpectedVMName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}

	diskCIDs := []apiv1.DiskCID{apiv1.NewDiskCID("disk-foo"), apiv1.NewDiskCID("disk-bar")}

	if version == "v1" {
		_, err = ts.v.CreateVM(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	} else {
		_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	}

	expectedError := "send agent setting to vm local path:"
	if err == nil {
		t.Fatal("test failed, got nil, expected error")
	}
	if !strings.Contains(err.Error(), expectedError) {
		t.Fatalf("got error '%s', expected error '%s'", err.Error(), expectedError)
	}
}

func TestGetVMIP(t *testing.T) {
	ip := "1.2.3.4"

	testCases := map[string]struct {
		netType    string
		ip         string
		expectedIP string
	}{
		"manual with ip set": {
			netType:    "manual",
			ip:         ip,
			expectedIP: ip,
		},
		"manual with ip unset": {
			netType:    "manual",
			ip:         "",
			expectedIP: "",
		},
		"dynamic with ip set": {
			netType:    "dynamic",
			ip:         ip,
			expectedIP: "",
		},
		"vip with ip set": {
			netType:    "vip",
			ip:         ip,
			expectedIP: "",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			network, err := getNetworks(networkConfig{
				netType: tc.netType,
				ip:      tc.ip,
			})
			if err != nil {
				t.Fatalf("got network: %v", err)
			}
			gotIP, _, _, _ := getNetwork(network)
			if gotIP != tc.expectedIP {
				t.Fatalf("got %s, expected %s", gotIP, tc.expectedIP)
			}
		})
	}
}

func TestCreateVMFailed_SubnetNoNetworkIP(t *testing.T) {
	vmName := "test-vm"
	diskCIDs := []apiv1.DiskCID{}
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
		SubnetName:          "subnet-name",
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}

	fakeFinder := &errorObjectFinder{}
	fc := test.FakeClient()
	v := NewVMs(fc, projectID, uuidGen, agentOpt, []config.ServerPort{}, logger, fakeFinder, aes, &mockImageService{}, &wait.Waiter{}, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)

	_, _, err = v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	if err == nil {
		t.Fatal("got nil, expected error")
	}

	expectedError := "network.IP must be specified when vm prop SubnetName is provided"
	gotError := err.Error()
	if gotError != expectedError {
		t.Fatalf("got error %q, expected error %q", gotError, expectedError)
	}
}

func TestCreateVMV1_Failed_DiskNotFound(t *testing.T) {
	testCreateVMFailed_DiskNotFound("v1", t)
}

func TestCreateVMV2_Failed_DiskNotFound(t *testing.T) {
	testCreateVMFailed_DiskNotFound("v2", t)
}

func testCreateVMFailed_DiskNotFound(version string, t *testing.T) {
	disk := virtualMachineDisk("foo")
	ts := newDefaultTestSetup(disk)

	prop := Property{
		Name:               "test-vm",
		VirtualMachineType: "test-cpu-2",
		BootDiskSizeGB:     10,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}
	diskCIDs := []apiv1.DiskCID{apiv1.NewDiskCID("foo"), apiv1.NewDiskCID("bar")}

	if version == "v1" {
		_, err = ts.v.CreateVM(apiv1.AgentID{}, apiv1.NewStemcellCID("test"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	} else {
		_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("test"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	}
	if err == nil {
		t.Fatal("test failed, got nil, expected error")
	}
}

func TestCreateVMV1_Failed_StemcellNotFound(t *testing.T) {
	testCreateVMFailed_StemcellNotFound("v1", t)
}

func TestCreateVMV2_Failed_StemcellNotFound(t *testing.T) {
	testCreateVMFailed_StemcellNotFound("v2", t)
}

func testCreateVMFailed_StemcellNotFound(version string, t *testing.T) {
	ts := newDefaultTestSetup(virtualMachineDisk("foo"))
	ts.m.err = errors.New("stemcell does not exist")

	prop := Property{
		Name:               "test-vm",
		VirtualMachineType: "test-cpu-2",
		BootDiskSizeGB:     10,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}
	diskCIDs := []apiv1.DiskCID{apiv1.NewDiskCID("foo")}

	if version == "v1" {
		_, err = ts.v.CreateVM(apiv1.AgentID{}, apiv1.NewStemcellCID("test"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	} else {
		_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("test"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	}
	if err == nil {
		t.Fatal("test failed, got nil, expected error")
	}
}

func TestCreateVMV1_Failed(t *testing.T) {
	testCreateVMFailed("v1", t)
}

func TestCreateVMV2_Failed(t *testing.T) {
	testCreateVMFailed("v2", t)
}

func testCreateVMFailed(version string, t *testing.T) {
	ts := newDefaultTestSetup(virtualMachineDisk("disk-foo"), virtualMachineDisk("disk-bar"))
	ts.m.image = virtualMachineImage("image-foo")

	prop := Property{
		Name:               "test-vm",
		VirtualMachineType: "test-cpu-2",
		BootDiskSizeGB:     10,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}
	diskCIDs := []apiv1.DiskCID{apiv1.NewDiskCID("disk-foo")}

	if version == "v1" {
		_, err = ts.v.CreateVM(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	} else {
		_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	}
	if err == nil {
		t.Fatal("test failed, got nil, expected error")
	}

	verifyCleanup(ts.v.cloudClient, t)
}

func TestCreateVMV1_Failed_DiskNotReady(t *testing.T) {
	testCreateVMFailed_BootDiskNotReady("v1", t)
}

func TestCreateVMV2_Failed_BootDiskNotReady(t *testing.T) {
	testCreateVMFailed_BootDiskNotReady("v2", t)
}

func testCreateVMFailed_BootDiskNotReady(version string, t *testing.T) {
	ts := newDefaultTestSetup()
	ts.m.image = virtualMachineImage("image-foot")
	vmName := "test-vm"
	diskName := fmt.Sprintf("%s-boot-disk", vmName)
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 10,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}
	diskCIDs := []apiv1.DiskCID{}

	// erver side action updates boot disk to failed state
	action := func(ctx context.Context, c client.Client) error {
		disk := &vmv1.VirtualMachineDisk{}
		if err := c.Get(ctx, client.ObjectKey{
			Name:      diskName,
			Namespace: projectID,
		}, disk); err != nil {
			return fmt.Errorf("get disk: %v", err)
		}
		disk.Status.Phase = vmv1.DiskPhaseFailed

		if err := c.Update(ctx, disk); err != nil {
			return fmt.Errorf("update disk: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineDisk"] = action
	ts.w.TargetObjects["VirtualMachineDisk"] = &vmv1.VirtualMachineDisk{}

	if version == "v1" {
		_, err = ts.v.CreateVM(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	} else {
		_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	}
	if err != nil && !strings.Contains(err.Error(), "wait until disk") {
		t.Fatalf("got unexpected error: %v", err)
	}

	// verify boot disk gets cleaned up
	key := client.ObjectKey{
		Name:      diskName,
		Namespace: projectID,
	}
	disk := &vmv1.VirtualMachineDisk{}
	if err := ts.c.Get(ctx, key, disk); err != nil {
		if !apierrors.IsNotFound(err) {
			t.Fatalf("get disk: %v", err)
		}
	}
}

func TestCreateVM_CloudNATWithEmptyProp(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.v.useCloudNATGateways = true
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "cloud-nat-vm"

	// Mock waiter vmAction to make the VM creation succeed
	vmAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Conditions = conditionTypes
		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachine"] = vmAction
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	vmeaAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vmea := &vmv1.VirtualMachineExternalAccess{}
		if err := c.Get(ctx, key, vmea); err != nil {
			return fmt.Errorf("get VMEA: %v", err)
		}

		vmea.Status.IngressIP = mockIngressIP
		if err := c.Update(ctx, vmea); err != nil {
			return fmt.Errorf("update VMEA: %v", err)
		}
		// Verify VMEA has the Cloud NAT annotation if Cloud NAT is enabled as required.
		if annotation, ok := vmea.Annotations[consts.UseCloudNATEgressControlAnnotationKey]; !ok || annotation != "true" {
			t.Errorf("VM annotation for cloud NAT: '%s' (present: %t), want 'true'", annotation, ok)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineExternalAccess"] = vmeaAction
	ts.w.TargetObjects["VirtualMachineExternalAccess"] = &vmv1.VirtualMachineExternalAccess{}

	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}

	cid, _, err := ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("CreateVMV2 failed: %v", err)
	}

	// Verification
	vm, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("Could not find created VM %q: %v", cid.AsString(), err)
	}
	// Verify legacy egress label has been removed at VM level if Cloud NAT is enabled
	gotVmExternalEgressValue, ok := vm.Labels[consts.VMExternalEgressControlLabelKey]
	if ok {
		t.Fatalf("gotVmExternalEgressValue: %v, want: nil/empty if Cloud NAT enabled", gotVmExternalEgressValue)
	}
}

func TestCreateVM_CloudNATWithValidProp(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.v.useCloudNATGateways = true
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "cloud-nat-vm"

	// Mock waiter vmAction to make the VM creation succeed
	vmAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Conditions = conditionTypes
		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachine"] = vmAction
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	vmeaAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vmea := &vmv1.VirtualMachineExternalAccess{}
		if err := c.Get(ctx, key, vmea); err != nil {
			return fmt.Errorf("get VMEA: %v", err)
		}

		vmea.Status.IngressIP = mockIngressIP
		if err := c.Update(ctx, vmea); err != nil {
			return fmt.Errorf("update VMEA: %v", err)
		}
		// Verify VMEA has the Cloud NAT annotation if Cloud NAT is enabled as required.
		if annotation, ok := vmea.Annotations[consts.UseCloudNATEgressControlAnnotationKey]; !ok || annotation != "true" {
			t.Errorf("VM annotation for cloud NAT: '%s' (present: %t), want 'true'", annotation, ok)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineExternalAccess"] = vmeaAction
	ts.w.TargetObjects["VirtualMachineExternalAccess"] = &vmv1.VirtualMachineExternalAccess{}

	cn_selector_key := "cn_selector_key"
	cn_selector_value := "cn_selector_value"
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
		CloudNAT: CloudNAT{
			Name: "some-cn",
			Selector: CloudNATSelector{
				Key:   cn_selector_key,
				Value: cn_selector_value,
			},
		},
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}

	cid, _, err := ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("CreateVMV2 failed: %v", err)
	}

	// Verification
	vm, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("Could not find created VM %q: %v", cid.AsString(), err)
	}
	// Verify legacy egress label has been removed at VM level if Cloud NAT is enabled
	gotVmExternalEgressValue, ok := vm.Labels[consts.VMExternalEgressControlLabelKey]
	if ok {
		t.Fatalf("gotVmExternalEgressValue: %v, want: nil/empty if Cloud NAT enabled", gotVmExternalEgressValue)
	}
	// Verify VM label is set to Cloud NAT label selector
	if gotCNLabel, ok := vm.Labels[cn_selector_key]; ok {
		if gotCNLabel != cn_selector_value {
			t.Fatalf("vm.Labels[%v]=%v, want: %v", cn_selector_key, gotCNLabel, cn_selector_value)
		}
	}
}

func TestCreateVM_CloudNATWithLabelOverrideFromProp(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.v.useCloudNATGateways = true
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "cloud-nat-vm"

	// Mock waiter vmAction to make the VM creation succeed
	vmAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Conditions = conditionTypes
		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachine"] = vmAction
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	vmeaAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vmea := &vmv1.VirtualMachineExternalAccess{}
		if err := c.Get(ctx, key, vmea); err != nil {
			return fmt.Errorf("get VMEA: %v", err)
		}

		vmea.Status.IngressIP = mockIngressIP
		if err := c.Update(ctx, vmea); err != nil {
			return fmt.Errorf("update VMEA: %v", err)
		}
		// Verify VMEA has the Cloud NAT annotation if Cloud NAT is enabled as required.
		if annotation, ok := vmea.Annotations[consts.UseCloudNATEgressControlAnnotationKey]; !ok || annotation != "true" {
			t.Errorf("VM annotation for cloud NAT: '%s' (present: %t), want 'true'", annotation, ok)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineExternalAccess"] = vmeaAction
	ts.w.TargetObjects["VirtualMachineExternalAccess"] = &vmv1.VirtualMachineExternalAccess{}

	cn_selector_key := "cn_selector_key"
	cn_selector_value := "cn_selector_value"
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
		Labels: map[string]string{
			"cn_selector_key": "diff_value",
		},
		CloudNAT: CloudNAT{
			Name: "some-cn",
			Selector: CloudNATSelector{
				Key:   cn_selector_key,
				Value: cn_selector_value,
			},
		},
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}

	cid, _, err := ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("CreateVMV2 failed: %v", err)
	}

	// Verification
	vm, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("Could not find created VM %q: %v", cid.AsString(), err)
	}
	// Verify legacy egress label has been removed at VM level if Cloud NAT is enabled
	gotVmExternalEgressValue, ok := vm.Labels[consts.VMExternalEgressControlLabelKey]
	if ok {
		t.Fatalf("gotVmExternalEgressValue: %v, want: nil/empty if Cloud NAT enabled", gotVmExternalEgressValue)
	}
	// Verify Cloud NAT selector label overrides the existing VM label for Cloud NAT (takes precedence)
	if gotCNLabel, ok := vm.Labels[cn_selector_key]; ok {
		if gotCNLabel != cn_selector_value {
			t.Fatalf("vm.Labels[%v]=%v, want: %v", cn_selector_key, gotCNLabel, cn_selector_value)
		}
	}
}

func TestCreateVM_WithUseCloudNATGatewaysDisabled(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.v.useCloudNATGateways = false
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "cloud-nat-vm"

	// Mock waiter vmAction to make the VM creation succeed
	vmAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Conditions = conditionTypes
		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachine"] = vmAction
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	vmeaAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vmea := &vmv1.VirtualMachineExternalAccess{}
		if err := c.Get(ctx, key, vmea); err != nil {
			return fmt.Errorf("get VMEA: %v", err)
		}

		vmea.Status.IngressIP = mockIngressIP
		if err := c.Update(ctx, vmea); err != nil {
			return fmt.Errorf("update VMEA: %v", err)
		}
		// Verify VMEA does not have the Cloud NAT annotation.
		if annotation, ok := vmea.Annotations[consts.UseCloudNATEgressControlAnnotationKey]; ok || annotation == "true" {
			t.Errorf("VM annotation for cloud NAT: '%s' (present: %t), want: empty", annotation, ok)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineExternalAccess"] = vmeaAction
	ts.w.TargetObjects["VirtualMachineExternalAccess"] = &vmv1.VirtualMachineExternalAccess{}

	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}

	cid, _, err := ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("CreateVMV2 failed: %v", err)
	}

	// Verification
	vm, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("Could not find created VM %q: %v", cid.AsString(), err)
	}
	// Verify legacy egress label is present (not removed)
	gotVmExternalEgressValue, ok := vm.Labels[consts.VMExternalEgressControlLabelKey]
	if !ok || gotVmExternalEgressValue != "true" {
		t.Fatalf("gotVmExternalEgressValue: %v (present: %v), want: true", gotVmExternalEgressValue, ok)
	}
}

func TestCreateVM_WithPublicKey_TriggersVMAR(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.m.image = virtualMachineImage("image-foo")
	ts.v.access.externalAccessPorts = []config.ServerPort{}

	mockVMARSvc := &mockVMAccessChecker{}
	ts.vmar = mockVMARSvc
	ts.v.vmAccessService = mockVMARSvc

	vmName := "byo-publickey-vm"
	publicKey := "ssh-rsa AAAAB3NzaC1yc..."
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 10,
		PublicKey:           publicKey,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}

	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{Name: vmName, Namespace: ts.v.projectID}
		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return err
		}
		vm.Status = vmv1.VirtualMachineStatus{
			State: vmv1.VirtualMachineStateRunning,
			Conditions: []metav1.Condition{
				{
					Type:   vmv1.ConditionTypeGuestEnvironmentSynced,
					Status: metav1.ConditionTrue,
				},
			},
			Network: vmv1.NetworkStatus{
				Interfaces: []vmv1.NetworkInterfaceStatus{
					{
						Name:        "net1",
						IpAddresses: []string{"10.0.0.10/24"},
					},
				},
			},
		}
		return c.Update(ctx, vm)
	}
	ts.w.ServerSideActions["VirtualMachine"] = action
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("CreateVMV2 failed: %v", err)
	}

	// Verify VMAR was created with the correct public key.
	if !ts.vmar.ensureVMExistAndReadyCalled {
		t.Error("EnsureVMARExistAndReady was not called, but was expected to")
	}
	if ts.vmar.vmName != vmName {
		t.Errorf("EnsureVMARExistAndReady called with vmName %q, expected %q", ts.vmar.vmName, vmName)
	}
	if ts.vmar.sshPublicKey != publicKey {
		t.Errorf("EnsureVMARExistAndReady called with publicKey %q, expected %q", ts.vmar.sshPublicKey, publicKey)
	}
}

func TestCreateVM_WithoutPublicKey_DoesNotTriggerVMAR(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.m.image = virtualMachineImage("image-foo")
	ts.v.access.externalAccessPorts = []config.ServerPort{}

	mockVMARSvc := &mockVMAccessChecker{}
	ts.vmar = mockVMARSvc
	ts.v.vmAccessService = mockVMARSvc

	vmName := "no-publickey-vm"
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 10,
		// PublicKey intentionally omitted
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}

	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{Name: vmName, Namespace: ts.v.projectID}
		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return err
		}
		vm.Status = vmv1.VirtualMachineStatus{
			State: vmv1.VirtualMachineStateRunning,
			Conditions: []metav1.Condition{
				{
					Type:   vmv1.ConditionTypeGuestEnvironmentSynced,
					Status: metav1.ConditionTrue,
				},
			},
			Network: vmv1.NetworkStatus{
				Interfaces: []vmv1.NetworkInterfaceStatus{
					{
						Name:        "net1",
						IpAddresses: []string{"10.0.0.10/24"},
					},
				},
			},
		}
		return c.Update(ctx, vm)
	}
	ts.w.ServerSideActions["VirtualMachine"] = action
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("CreateVMV2 failed: %v", err)
	}

	// Verify VMAR is NOT created when public key VM property is not provided.
	if ts.vmar.ensureVMExistAndReadyCalled {
		t.Error("EnsureVMARExistAndReady was called, but was NOT expected to")
	}
}

func TestCreateVM_WithDiskHotplug(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.v.useDiskHotplug = true
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "hotplug-vm"

	// Mock waiter vmAction to make the VM creation succeed
	vmAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Conditions = conditionTypes
		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachine"] = vmAction
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	vmeaAction := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vmea := &vmv1.VirtualMachineExternalAccess{}
		if err := c.Get(ctx, key, vmea); err != nil {
			return fmt.Errorf("get VMEA: %v", err)
		}

		vmea.Status.IngressIP = mockIngressIP
		if err := c.Update(ctx, vmea); err != nil {
			return fmt.Errorf("update VMEA: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineExternalAccess"] = vmeaAction
	ts.w.TargetObjects["VirtualMachineExternalAccess"] = &vmv1.VirtualMachineExternalAccess{}

	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}

	cid, _, err := ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("CreateVMV2 failed: %v", err)
	}

	// Verification
	vm, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("Could not find created VM %q: %v", cid.AsString(), err)
	}

	if annotation, ok := vm.Annotations[consts.DiskHotPlugAnnotationKey]; !ok || annotation != "true" {
		t.Errorf("Got hotpluggable-disks annotation '%s' (present: %t), want 'true'", annotation, ok)
	}
}

func TestCreateVM_Fail_ConflictingLabelWithLBSelector(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.v.access.externalAccessPorts = []config.ServerPort{}
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "test-vm"
	diskCIDs := []apiv1.DiskCID{}
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
		Labels: map[string]string{
			"lb_selector_key": "diff_value",
		},
		LoadBalancer: LB{
			Name: "some-lb",
			Selector: LBSelector{
				Key:   "lb_selector_key",
				Value: "lb_selector_value",
			},
		},
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("convert to vmCloudProp from %v", prop)
	}

	v := NewVMs(ts.c, projectID, uuidGen, agentOpt, []config.ServerPort{}, logger, ts.f, aes, ts.m, ts.w, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)
	expectedError := "has specified label conflicting with LB"
	if _, _, err := v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{}); err == nil {
		t.Fatal("got error, expected nil")
	} else if !strings.Contains(err.Error(), expectedError) {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestCreateEphemeralDisk_Timeout(t *testing.T) {
	ts := newDefaultTestSetup()
	vmName := "test-vm"
	diskName := fmt.Sprintf("%s-ephemeral-disk", vmName)
	diskType := consts.DiskTypeStandard
	// server side action updates ephemeral disk to failed state
	action := func(ctx context.Context, c client.Client) error {
		disk := &vmv1.VirtualMachineDisk{}
		if err := c.Get(ctx, client.ObjectKey{
			Name:      diskName,
			Namespace: projectID,
		}, disk); err != nil {
			return fmt.Errorf("get disk: %v", err)
		}
		disk.Status.Phase = vmv1.DiskPhaseFailed

		if err := c.Update(ctx, disk); err != nil {
			return fmt.Errorf("update disk: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineDisk"] = action
	ts.w.TargetObjects["VirtualMachineDisk"] = &vmv1.VirtualMachineDisk{}

	if err := ts.v.createEphemeralDisk(ctx, diskName, vmName, 10, diskType); err != nil && !strings.Contains(err.Error(), "wait until disk") {
		t.Fatalf("got unexpected error: %v", err)
	}

	// verify ephemeral disk gets cleaned up
	key := client.ObjectKey{
		Name:      diskName,
		Namespace: projectID,
	}
	disk := &vmv1.VirtualMachineDisk{}
	if err := ts.c.Get(ctx, key, disk); err != nil {
		if !apierrors.IsNotFound(err) {
			t.Fatalf("get disk: %v", err)
		}
	}
}

func TestCreateVM_Succeed_WithStaticIP(t *testing.T) {
	fc := test.FakeClient()
	ts := newTestSetup(fc)

	ts.v.access.externalAccessPorts = []config.ServerPort{}
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "test-vm"
	gatewayIP := "1.2.3.1"
	diskCIDs := []apiv1.DiskCID{}
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}
	netStatus := []vmv1.NetworkInterfaceStatus{
		{
			Name: vmNodeNetworkLancerInterfaceName,
			IpAddresses: []string{
				"1.2.3.4/8",
			},
		},
	}

	networks, err := getNetworks(networkConfig{
		netType:    "manual",
		ip:         "1.2.3.4",
		gateway:    gatewayIP,
		subnetName: "subnet-name",
	})
	if err != nil {
		t.Fatalf("get network properties %v", err)
	}

	// server side action updates VM to running state and sets IP of the VM object.
	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}
		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Network.Interfaces = netStatus
		vm.Status.Conditions = conditionTypes

		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}

	ts.w.ServerSideActions["VirtualMachine"] = action
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}
	v := NewVMs(ts.c, projectID, uuidGen, agentOpt, []config.ServerPort{}, logger, ts.f, aes, ts.m, ts.w, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)

	cid, _, err := v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, networks, diskCIDs, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	// Verify VM
	got, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("unable to find VM %q", cid.AsString())
	}

	gotSubnet := got.Spec.Network.Interfaces[0].Subnet
	if gotSubnet != "subnet-name" {
		t.Fatalf("got subnetname %q, expect %q", gotSubnet, "subnet-name")
	}
}

func TestCreateVM_Succeed_WithSubnetAndLabelsDfeinedInNetwork(t *testing.T) {
	ts := newDefaultTestSetup()

	ts.v.access.externalAccessPorts = []config.ServerPort{}
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "test-vm"
	diskCIDs := []apiv1.DiskCID{}
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
		Labels: map[string]string{
			"label1": "original_value",
			"label2": "key2",
		},
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("failed to convert to vmCloudProp from %v", prop)
	}
	netStatus := []vmv1.NetworkInterfaceStatus{
		{
			Name: vmNodeNetworkLancerInterfaceName,
			IpAddresses: []string{
				"1.2.3.4/8",
			},
		},
	}

	networks, err := getNetworks(networkConfig{
		netType:    "manual",
		ip:         "1.2.3.4",
		subnetName: "cf-net-z1",
		labels: map[string]string{
			"label1": "overidden_value",
		},
	})

	if err != nil {
		t.Fatalf("failed to convert to network cloud properties from %v", prop)
	}

	// server side action updates VM to running state and sets IP of the VM object.
	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}
		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Network.Interfaces = netStatus
		vm.Status.Conditions = conditionTypes

		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}

	ts.w.ServerSideActions["VirtualMachine"] = action
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}
	v := NewVMs(ts.c, projectID, uuidGen, agentOpt, []config.ServerPort{}, logger, ts.f, aes, ts.m, ts.w, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)

	cid, _, err := v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, networks, diskCIDs, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	// Verify VM and labels defined from BOSH network takes precedence
	if gotVM, err := ts.f.FindVM(ctx, cid.AsString()); err != nil {
		t.Fatalf("unable to find VM %q", cid.AsString())
	} else if gotVM.Labels["label1"] != "overidden_value" {
		t.Fatalf("got value %q for the label key 'label1', expect value of %q", gotVM.Labels["label1"], "overidden_value")
	}
}

func TestCreateVM_Succeed_NoVMEA_NoStaticIP(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.v.access.externalAccessPorts = []config.ServerPort{}
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "test-vm"
	diskCIDs := []apiv1.DiskCID{}
	prop := Property{
		Name:                vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}
	netStatus := []vmv1.NetworkInterfaceStatus{
		{
			Name: vmNodeNetworkLancerInterfaceName,
			IpAddresses: []string{
				"1.2.3.4/8",
			},
		},
	}

	// server side action updates VM to running state and sets IP of the VM object.
	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}
		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Network.Interfaces = netStatus
		vm.Status.Conditions = conditionTypes

		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}

	ts.w.ServerSideActions["VirtualMachine"] = action
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}
	v := NewVMs(ts.c, projectID, uuidGen, agentOpt, []config.ServerPort{}, logger, ts.f, aes, ts.m, ts.w, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)

	cid, _, err := v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotVM, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("unable to find VM %q", cid.AsString())
	}
	if diff := cmp.Diff(netStatus, gotVM.Status.Network.Interfaces); diff != "" {
		t.Errorf("VM IP diff (-want +got):\n%s", diff)
	}
}

func TestCreateVM_Tier1Network(t *testing.T) {
	testCases := map[string]struct {
		name           string
		globalTier1    bool
		propTier1      bool
		machineType    string
		vCPUs          uint32
		expectError    bool
		expectTier1Set bool
	}{
		"Global true, Prop true, Eligible VM -> Tier 1 set, no error": {
			globalTier1:    true,
			propTier1:      true,
			machineType:    "n2-standard-8",
			vCPUs:          8,
			expectError:    false,
			expectTier1Set: true,
		},
		"Global true, Prop true, Ineligible VM -> Error": {
			globalTier1:    true,
			propTier1:      true,
			machineType:    "n2-standard-4",
			vCPUs:          4,
			expectError:    true,
			expectTier1Set: false,
		},
		"Global true, Prop false, Eligible VM -> Tier 1 set, no error": {
			globalTier1:    true,
			propTier1:      false,
			machineType:    "n2-standard-8",
			vCPUs:          8,
			expectError:    false,
			expectTier1Set: true,
		},
		"Global true, Prop false, Ineligible VM -> Error": {
			globalTier1:    true,
			propTier1:      false,
			machineType:    "n2-standard-4",
			vCPUs:          4,
			expectError:    true,
			expectTier1Set: false,
		},
		"Global false, Prop true, Eligible VM -> Tier 1 set, no error": {
			globalTier1:    false,
			propTier1:      true,
			machineType:    "n2-standard-8",
			vCPUs:          8,
			expectError:    false,
			expectTier1Set: true,
		},
		"Global false, Prop true, Ineligible VM -> Error": {
			globalTier1:    false,
			propTier1:      true,
			machineType:    "n2-standard-4",
			vCPUs:          4,
			expectError:    true,
			expectTier1Set: false,
		},
		"Global false, Prop false, Eligible VM -> Tier 1 not set, no error": {
			globalTier1:    false,
			propTier1:      false,
			machineType:    "n2-standard-8",
			vCPUs:          8,
			expectError:    false,
			expectTier1Set: false,
		},
		"Global false, Prop false, Ineligible VM -> Tier 1 not set, no error": {
			globalTier1:    false,
			propTier1:      false,
			machineType:    "n2-standard-4",
			vCPUs:          4,
			expectError:    false,
			expectTier1Set: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			vmName := "test-vm"
			n2Standard8 := &vmv1.VirtualMachineType{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "n2-standard-8",
					Namespace: "vm-system",
				},
				Spec: vmv1.VirtualMachineTypeSpec{
					VCPUs:  8,
					Memory: resource.MustParse("32Gi"),
				},
			}
			n2Standard4 := &vmv1.VirtualMachineType{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "n2-standard-4",
					Namespace: "vm-system",
				},
				Spec: vmv1.VirtualMachineTypeSpec{
					VCPUs:  4,
					Memory: resource.MustParse("16Gi"),
				},
			}
			fc := test.FakeClient(n2Standard8, n2Standard4)
			ts := newTestSetup(fc)
			ts.v.useTier1Network = tc.globalTier1
			ts.m.image = virtualMachineImage("image-foo")

			// Mock waiter vmAction to make the VM creation succeed
			vmAction := func(ctx context.Context, c client.Client) error {
				key := client.ObjectKey{
					Name:      vmName,
					Namespace: projectID,
				}

				vm := &vmv1.VirtualMachine{}
				if err := c.Get(ctx, key, vm); err != nil {
					return fmt.Errorf("get VM: %v", err)
				}

				vm.Status.State = vmv1.VirtualMachineStateRunning
				vm.Status.Conditions = conditionTypes
				if err := c.Update(ctx, vm); err != nil {
					return fmt.Errorf("update VM: %v", err)
				}
				return nil
			}
			ts.w.ServerSideActions["VirtualMachine"] = vmAction
			ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

			vmeaAction := func(ctx context.Context, c client.Client) error {
				key := client.ObjectKey{
					Name:      vmName,
					Namespace: projectID,
				}

				vmea := &vmv1.VirtualMachineExternalAccess{}
				if err := c.Get(ctx, key, vmea); err != nil {
					return fmt.Errorf("get VMEA: %v", err)
				}

				vmea.Status.IngressIP = mockIngressIP
				if err := c.Update(ctx, vmea); err != nil {
					return fmt.Errorf("update VMEA: %v", err)
				}
				return nil
			}
			ts.w.ServerSideActions["VirtualMachineExternalAccess"] = vmeaAction
			ts.w.TargetObjects["VirtualMachineExternalAccess"] = &vmv1.VirtualMachineExternalAccess{}

			prop := Property{
				Name:                vmName,
				VirtualMachineType:  tc.machineType,
				VCPUs:               int(tc.vCPUs),
				BootDiskSizeGB:      10,
				EphemeralDiskSizeGB: 20,
				UseTier1Network:     tc.propTier1,
			}
			vmProp, err := convert(prop)
			if err != nil {
				t.Fatalf("failed to convert to vmCloudProp from %v: %v", prop, err)
			}

			_, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})

			if tc.expectError {
				if err == nil {
					t.Fatal("Got: nil error, want: error for VM that is ineligible tier 1 network")
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
				// Verification
				vm, err := ts.f.FindVM(ctx, vmName)
				if err != nil {
					t.Fatalf("Could not find created VM %q: %v", vmName, err)
				}

				expectedTier := ""
				if tc.expectTier1Set {
					expectedTier = consts.NetworkPerformanceTier1
				}

				if !tc.expectTier1Set && vm.Spec.Network == nil {
					// This is the expected outcome for non-Tier 1 cases, so do nothing.
				} else if vm.Spec.Network == nil || vm.Spec.Network.NetworkPerformanceTier != vmv1.NetworkPerformanceTier(expectedTier) {
					t.Errorf("Got NetworkPerformanceTier '%s', want '%s'", vm.Spec.Network.NetworkPerformanceTier, expectedTier)
				}
			}
		})
	}
}

func TestCreateVM_Succeed_WithCustomVMSpec(t *testing.T) {
	ts := newDefaultTestSetup()
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "test-vm"
	vCPUs := 100
	memorySize := 200

	prop := Property{
		Name:                vmName,
		VCPUs:               vCPUs,
		MemorySizeGB:        memorySize,
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}

	// server side action updates VMEA
	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vmea := &vmv1.VirtualMachineExternalAccess{}
		if err := c.Get(ctx, key, vmea); err != nil {
			return fmt.Errorf("get VMEA: %v", err)
		}

		vmea.Status.IngressIP = mockIngressIP
		if err := c.Update(ctx, vmea); err != nil {
			return fmt.Errorf("update VMEA: %v", err)
		}
		return nil
	}
	ts.w.ServerSideActions["VirtualMachineExternalAccess"] = action
	ts.w.TargetObjects["VirtualMachineExternalAccess"] = &vmv1.VirtualMachineExternalAccess{}

	// server side action updates VM to running state
	action = func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}

		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Conditions = conditionTypes
		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}

		return nil
	}
	ts.w.ServerSideActions["VirtualMachine"] = action
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}

	var cid apiv1.VMCID
	cid, _, err = ts.v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, []apiv1.DiskCID{}, apiv1.VMEnv{})

	if err != nil {
		t.Fatalf("test failed, got unexpected error: %v", err)
	}

	if cid.AsString() != vmName {
		t.Fatalf("got vm name %s, expected %s", cid.AsString(), vmName)
	}

	gotVM, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("unable to find VM %q %v", cid.AsString(), gotVM)
	}

	memoryDiskResource := resource.MustParse(fmt.Sprintf("%vGi", memorySize))
	if gotVM.Spec.Compute.Memory.Cmp(memoryDiskResource) != 0 {
		t.Fatalf("got memory size %v, want %v", gotVM.Spec.Compute.Memory, memoryDiskResource)
	}

	if gotVM.Spec.Compute.VCPUs != uint32(vCPUs) {
		t.Fatalf("got vCPUs %v, want %v", gotVM.Spec.Compute.VCPUs, memoryDiskResource)
	}
}

func TestCreateVM_Succeed_WithZoneLabel(t *testing.T) {
	zone := "test-zone"
	ts := newDefaultTestSetup()
	ts.m.image = virtualMachineImage("image-foo")
	vmName := "test-vm-z1"
	diskCIDs := []apiv1.DiskCID{}
	prop := Property{
		Name:                vmName,
		Zone:                zone,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
	}
	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("test failed, failed to convert to vmCloudProp from %v", prop)
	}
	netStatus := []vmv1.NetworkInterfaceStatus{
		{
			Name: vmNodeNetworkLancerInterfaceName,
			IpAddresses: []string{
				"1.2.3.4/8",
			},
		},
	}

	// server side action updates VM to running state and sets IP of the VM object.
	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      vmName,
			Namespace: projectID,
		}
		vm := &vmv1.VirtualMachine{}
		if err := c.Get(ctx, key, vm); err != nil {
			return fmt.Errorf("get VM: %v", err)
		}

		vm.Status.State = vmv1.VirtualMachineStateRunning
		vm.Status.Network.Interfaces = netStatus
		vm.Status.Conditions = conditionTypes

		if err := c.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM: %v", err)
		}
		return nil
	}

	ts.w.ServerSideActions["VirtualMachine"] = action
	ts.w.TargetObjects["VirtualMachine"] = &vmv1.VirtualMachine{}
	v := NewVMs(ts.c, projectID, uuidGen, agentOpt, []config.ServerPort{}, logger, ts.f, aes, ts.m, ts.w, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)

	cid, _, err := v.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}

	gotVM, err := ts.f.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("unable to find VM %q", cid.AsString())
	}
	if gotVM.Labels[consts.ZoneKey] != zone {
		t.Fatalf("got zone %q, want %q", gotVM.Labels, zone)
	}
}

type createVMTestCase struct {
	name              string
	vmName            string
	version           string
	bootDiskType      string
	ephemeralDiskType string
}

func TestCreateVMSuccessAndCleanup(t *testing.T) {
	testCases := []createVMTestCase{
		{
			name:              "v1 API with standard disks",
			vmName:            "test-vm-v1-std",
			version:           "v1",
			bootDiskType:      consts.DiskTypeStandard,
			ephemeralDiskType: consts.DiskTypeStandard,
		},
		{
			name:              "v2 API with standard disks",
			vmName:            "test-vm-v2-std",
			version:           "v2",
			bootDiskType:      consts.DiskTypeStandard,
			ephemeralDiskType: consts.DiskTypeStandard,
		},
		{
			name:              "v2 API with performance disks",
			vmName:            "test-vm-v2-perf",
			version:           "v2",
			bootDiskType:      consts.DiskTypePerformance,
			ephemeralDiskType: consts.DiskTypePerformance,
		},
		{
			name:              "v2 API with mixed disks (perf boot, std ephemeral)",
			vmName:            "test-vm-v2-mixed1",
			version:           "v2",
			bootDiskType:      consts.DiskTypePerformance,
			ephemeralDiskType: consts.DiskTypeStandard,
		},
		{
			name:              "v2 API with mixed disks (std boot, perf ephemeral)",
			vmName:            "test-vm-v2-mixed2",
			version:           "v2",
			bootDiskType:      consts.DiskTypeStandard,
			ephemeralDiskType: consts.DiskTypePerformance,
		},
		{
			name:              "v2 API with generated name",
			vmName:            "", // Empty name triggers generation
			version:           "v2",
			bootDiskType:      consts.DiskTypeStandard,
			ephemeralDiskType: consts.DiskTypeStandard,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ts := newDefaultTestSetup(virtualMachineDisk("disk-foo"), virtualMachineDisk("disk-bar"))
			ts.m.image = virtualMachineImage("image-foo")

			expectedVMName := tc.vmName
			if len(tc.vmName) == 0 {
				expectedVMName = mockExpectedVMName
			}

			// server side action updates VM to running state sets the ingress IP of the VMEA object.
			action := func(ctx context.Context, c client.Client) error {
				key := client.ObjectKey{
					Name:      expectedVMName,
					Namespace: projectID,
				}

				vm := &vmv1.VirtualMachine{}
				if err := c.Get(ctx, key, vm); err != nil {
					return fmt.Errorf("c.Get(%v, %v)=%v", key, vm, err)
				}

				vm.Status.State = vmv1.VirtualMachineStateRunning
				if err := c.Update(ctx, vm); err != nil {
					return fmt.Errorf("c.Update(%v)=%v", vm, err)
				}

				vmea := &vmv1.VirtualMachineExternalAccess{}
				if err := c.Get(ctx, key, vmea); err != nil {
					return fmt.Errorf("c.Get(%v, %v)=%v", key, vmea, err)
				}

				vmea.Status.IngressIP = mockIngressIP
				if err := c.Update(ctx, vmea); err != nil {
					return fmt.Errorf("c.Update(%v)=%v", vmea, err)
				}
				return nil
			}
			ts.w.ServerSideActions["VirtualMachineExternalAccess"] = action
			ts.w.TargetObjects["VirtualMachineExternalAccess"] = &vmv1.VirtualMachineExternalAccess{}

			vmProp := buildVMProps(t, tc)
			diskCIDs := []apiv1.DiskCID{apiv1.NewDiskCID("disk-foo"), apiv1.NewDiskCID("disk-bar")}

			cid, err := performCreateVMCall(ts.v, tc.version, vmProp, diskCIDs)
			if err != nil {
				t.Fatalf("performCreateVMCall(%v, %v, %v, %v)=%v", ts.v, tc.version, vmProp, diskCIDs, err)
			}

			vm := assertVM(t, ts.f, cid, expectedVMName)

			// Verify disks
			bootDiskName := fmt.Sprintf("%s-boot-disk", vm.Name)
			ephemeralDiskName := fmt.Sprintf("%s-ephemeral-disk", vm.Name)
			assertDisk(t, ts.f, bootDiskName, resource.MustParse("10Gi"), vmv1.DiskType(tc.bootDiskType))
			assertDisk(t, ts.f, ephemeralDiskName, resource.MustParse("20Gi"), vmv1.DiskType(tc.ephemeralDiskType))

			// Verify CRs for vm access
			verifyVMAccess(ts.v.access, vm.Name, mockIngressIP, t)

			// Clearnup and verify
			ts.v.cleanupAll(ctx, vm)
			verifyCleanup(ts.v.cloudClient, t)
		})
	}
}

func assertVM(t *testing.T, finder *service.CPIFinder, cid apiv1.VMCID, wantVMName string) *vmv1.VirtualMachine {
	t.Helper()
	if cid.AsString() != wantVMName {
		t.Fatalf("cid.AsString()=%s, want: %s", cid.AsString(), wantVMName)
	}

	vm, err := finder.FindVM(ctx, cid.AsString())
	if err != nil {
		t.Fatalf("finder.FindVM(%s)=%v", cid.AsString(), err)
	}

	bootDiskName := fmt.Sprintf("%s-boot-disk", vm.Name)
	ephemeralDiskName := fmt.Sprintf("%s-ephemeral-disk", vm.Name)
	expectedVM := buildExpectedVM(wantVMName, bootDiskName, ephemeralDiskName)

	if diff := cmp.Diff(expectedVM, *vm, cmpopts.IgnoreFields(vmv1.VirtualMachine{}, "TypeMeta", "ObjectMeta.ResourceVersion")); diff != "" {
		t.Errorf("vm diff (-want +got):\n%s", diff)
	}
	return vm
}

func buildVMProps(t *testing.T, tc createVMTestCase) apiv1.VMCloudProps {
	t.Helper()
	prop := Property{
		Name:                tc.vmName,
		VirtualMachineType:  "test-cpu-2",
		BootDiskSizeGB:      10,
		EphemeralDiskSizeGB: 20,
		BootDiskType:        tc.bootDiskType,
		EphemeralDiskType:   tc.ephemeralDiskType,
		Labels: map[string]string{
			"key1": "value1",
			"key2": "value12",
		},
		AccessEnabled: true,
		Firmware: VMFirmware{
			Type:             "type-foo",
			EnableSecureBoot: false,
		},
		LoadBalancer: LB{
			Name: "some-lb",
			Selector: LBSelector{
				Key:   "instance_group",
				Value: "bosh",
			},
		},
	}

	vmProp, err := convert(prop)
	if err != nil {
		t.Fatalf("convert(%v)=%v", prop, err)
	}
	return vmProp
}

func performCreateVMCall(vms *VMs, version string, vmProp apiv1.VMCloudProps, diskCIDs []apiv1.DiskCID) (apiv1.VMCID, error) {
	if version == "v1" {
		return vms.CreateVM(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	}
	cid, _, err := vms.CreateVMV2(apiv1.AgentID{}, apiv1.NewStemcellCID("image-foo"), vmProp, apiv1.Networks{}, diskCIDs, apiv1.VMEnv{})
	return cid, err
}

func buildExpectedVM(vmName, bootDiskName, ephemeralDiskName string) vmv1.VirtualMachine {
	return vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:            vmName,
			Namespace:       projectID,
			ResourceVersion: "1",
			Labels: map[string]string{
				consts.VMExternalEgressControlLabelKey: "true",
				"key1":                                 "value1",
				"key2":                                 "value12",
				"instance_group":                       "bosh",
			},
		},
		Status: vmv1.VirtualMachineStatus{
			State: vmv1.VirtualMachineStateRunning,
		},
		Spec: vmv1.VirtualMachineSpec{
			Compute: vmv1.Compute{
				VirtualMachineType: "test-cpu-2",
			},
			Disks: []vmv1.DiskAttachment{
				{
					Boot:                  ptr.To(true),
					VirtualMachineDiskRef: corev1.LocalObjectReference{Name: bootDiskName},
					AutoDelete:            ptr.To(true),
				},
				{
					Boot:                  ptr.To(false),
					VirtualMachineDiskRef: corev1.LocalObjectReference{Name: ephemeralDiskName},
					AutoDelete:            ptr.To(true),
				},
			},
			GuestEnvironment: &vmv1.GuestEnvironment{
				AccessManagement: &vmv1.AccessManagementConfig{
					Enable: true,
				},
			},
			Firmware: &vmv1.Firmware{
				Type:             "type-foo",
				EnableSecureBoot: ptr.To(false),
			},
		},
	}
}

func assertDisk(t *testing.T, finder *service.CPIFinder, diskName string, wantSize resource.Quantity, wantType vmv1.DiskType) {
	t.Helper()
	disk, err := finder.FindDisk(ctx, diskName)
	if err != nil {
		t.Fatalf("finder.FindDisk(%s)=%v", diskName, err)
	}
	if disk.Spec.Size.Cmp(wantSize) != 0 {
		t.Errorf("Got disk size %v, want %v", disk.Spec.Size, wantSize)
	}
	if disk.Spec.Type != wantType {
		t.Errorf("Got disk type %v, want %v", disk.Spec.Type, wantType)
	}
}

func verifyVMAccess(a *Access, vmName string, ingressIP string, t *testing.T) {
	vmea := a.virtualMachineExternalAccess(vmName, false)
	vmea.Status.IngressIP = ingressIP
	foundVMEA := &vmv1.VirtualMachineExternalAccess{}
	if err := a.cloudClient.Get(ctx, client.ObjectKeyFromObject(vmea), foundVMEA); err != nil {
		t.Fatalf("a.cloudClient.Get(%s,%v)=%v", client.ObjectKeyFromObject(vmea), foundVMEA, err)
	}
	foundVMEA.TypeMeta = vmea.TypeMeta
	foundVMEA.ObjectMeta.ResourceVersion = ""
	if diff := cmp.Diff(vmea, foundVMEA); diff != "" {
		t.Errorf("test failed, VirtualMachineExternalAccess (-want +got):\n%s", diff)
	}
}

func verifyCleanup(c client.Client, t *testing.T) {
	foundVMEAList := &vmv1.VirtualMachineExternalAccessList{}
	if err := c.List(ctx, foundVMEAList, &client.ListOptions{
		Namespace: projectID}); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("test failed, failed to list VirtualMachineExternalAcccess: %v", err)
	} else if err == nil && len(foundVMEAList.Items) > 0 {
		t.Fatalf("test failed, VirtualMachineExternalAcccess %v found", foundVMEAList.Items)
	}

	foundVMList := &vmv1.VirtualMachineList{}
	if err := c.List(ctx, foundVMList, &client.ListOptions{
		Namespace: projectID}); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("test failed, failed to list VirtualMachine: %v", err)
	} else if err == nil && len(foundVMList.Items) > 0 {
		t.Fatalf("test failed, VirtualMachine %v found", foundVMList)
	}
}

// Test helpers
func convert(prop Property) (apiv1.VMCloudProps, error) {
	jsonData, err := json.Marshal(prop)
	if err != nil {
		return nil, err
	}

	return apiv1.CloudPropsImpl{
		RawMessage: json.RawMessage(jsonData),
	}, nil
}

type networkConfig struct {
	netType    string
	ip         string
	gateway    string
	subnetName string
	labels     map[string]string
}

func getNetworks(config networkConfig) (apiv1.Networks, error) {
	var networksData map[string]interface{}

	if err := json.Unmarshal([]byte(`{
		"test": {
			"type": "fake-type2",
			"ip": "1.2.3.4",
			"cloud_properties": {}
		}
	}`), &networksData); err != nil {
		return apiv1.Networks{}, fmt.Errorf("got unexpected error: %w", err)
	}

	networkData := networksData["test"].(map[string]interface{})
	networkData["type"] = config.netType
	networkData["ip"] = config.ip
	networkData["gateway"] = config.gateway

	netCloudProps := networkData["cloud_properties"].(map[string]interface{})
	netCloudProps["labels"] = config.labels

	if len(config.subnetName) > 0 {
		netCloudProps["subnetName"] = config.subnetName
	}

	networkData["cloud_properties"] = netCloudProps
	networksData["test"] = networkData

	networksJsonData, err := json.Marshal(networksData)
	if err != nil {
		return apiv1.Networks{}, fmt.Errorf("got unexpected error: %w", err)
	}

	networks := apiv1.Networks(make(map[string]apiv1.Network))
	if err := json.Unmarshal(networksJsonData, &networks); err != nil {
		return apiv1.Networks{}, fmt.Errorf("got unexpected error: %w", err)
	}

	return networks, nil
}

func getDefaultDisk() *vmv1.VirtualMachineDisk {
	return &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-disk", test.WantedObjName),
			Namespace: projectID,
		},
		Spec: vmv1.VirtualMachineDiskSpec{
			Size: resource.MustParse("10G"),
		},
	}
}

func getDefaultVM(diskName string, diskAutoDelete bool) *vmv1.VirtualMachine {
	return &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm-name",
			Namespace: projectID,
		},
		Spec: vmv1.VirtualMachineSpec{
			Compute: vmv1.Compute{},
			Disks: []vmv1.DiskAttachment{
				{
					VirtualMachineDiskRef: corev1.LocalObjectReference{Name: diskName},
					AutoDelete:            ptr.To(diskAutoDelete),
				},
			},
		},
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

func withLabels(labels map[string]string) option {
	return func(vm *vmv1.VirtualMachine) {
		vm.ObjectMeta.Labels = labels
	}
}

func withAnnotations(annotations map[string]string) option {
	return func(vm *vmv1.VirtualMachine) {
		vm.ObjectMeta.Annotations = annotations
	}
}

func withState(state vmv1.VirtualMachineState) option {
	return func(vm *vmv1.VirtualMachine) {
		vm.Status.State = state
	}
}

func virtualMachineType(cpu int, memroySize string) client.Object {
	return &vmv1.VirtualMachineType{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmTypeName(cpu, memroySize),
			Namespace: "vm-system",
		},
		Spec: vmv1.VirtualMachineTypeSpec{
			VCPUs:  uint32(cpu),
			Memory: resource.MustParse(memroySize),
		},
	}
}

func vmTypeName(cpu int, memorySize string) string {
	return fmt.Sprintf("cpu-%d-mem-%s", cpu, memorySize)
}

func virtualMachineImage(name string) *vmv1.VirtualMachineImage {
	return &vmv1.VirtualMachineImage{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VirtualMachineImage",
			APIVersion: "virtualmachine.gdc.goog/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: projectID,
		},
	}
}

type diskOption func(*vmv1.VirtualMachineDisk)

func virtualMachineDisk(name string, opts ...diskOption) *vmv1.VirtualMachineDisk {
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: projectID,
		},
		Spec: vmv1.VirtualMachineDiskSpec{
			Size: resource.MustParse("10G"),
		},
	}
	for _, apply := range opts {
		apply(disk)
	}
	return disk
}

func withDiskState(state vmv1.VirtualMachineDiskPhase) diskOption {
	return func(disk *vmv1.VirtualMachineDisk) {
		disk.Status.Phase = state
	}
}

// A mock implementation of `objectFinder` that always returns an error.
type errorObjectFinder struct{}

func (e *errorObjectFinder) HasVM(ctx context.Context, vmID string) (bool, error) {
	return false, errors.New("failed")
}

func (e *errorObjectFinder) FindVM(ctx context.Context, vmID string) (*vmv1.VirtualMachine, error) {
	return nil, errors.New("failed")
}

func (e *errorObjectFinder) FindDisk(ctx context.Context, diskID string) (*vmv1.VirtualMachineDisk, error) {
	return nil, errors.New("failed")
}

func (e *errorObjectFinder) FindStemcell(ctx context.Context, stemcellID string) (*vmv1.VirtualMachineImage, error) {
	return nil, errors.New("failed")
}

func (e *errorObjectFinder) FindVMIP(ctx context.Context, vmID string) (string, error) {
	return "", errors.New("failed")
}

func (e *errorObjectFinder) FindVMIngressIP(ctx context.Context, vmID string) (string, error) {
	return "", errors.New("failed")
}

type retryObjectFinder struct {
	*service.CPIFinder
	calls int
}

func (r *retryObjectFinder) FindVM(ctx context.Context, vmID string) (*vmv1.VirtualMachine, error) {
	r.calls++
	if r.calls == 1 {
		return nil, errors.New("Cannot find VM")
	}
	return r.CPIFinder.FindVM(ctx, vmID)
}

type mockSSH struct{}

func (m *mockSSH) Upload(_ context.Context, _, _, _, _ string) error { return nil }

func (m *mockSSH) Download(_ context.Context, _, _, _ string) ([]byte, error) { return nil, nil }

type mockErrorSSH struct{}

func (m *mockErrorSSH) Upload(_ context.Context, _, _, _, _ string) error {
	return errors.New("ssh upload failure")
}

func (m *mockErrorSSH) Download(_ context.Context, _, _, _ string) ([]byte, error) {
	return nil, errors.New("ssh download failure")
}

type mockImageService struct {
	err   error
	image *vmv1.VirtualMachineImage
}

func (m *mockImageService) Get(context.Context, string) (*vmv1.VirtualMachineImage, error) {
	return m.image, m.err
}

type testSetup struct {
	c    client.Client
	f    *service.CPIFinder
	m    *mockImageService
	v    *VMs
	w    *mock.MockWaiter
	vmar *mockVMAccessChecker
}

func newDefaultTestSetup(objs ...client.Object) *testSetup {
	c := test.FakeClient(objs...)
	return newTestSetup(c)
}

func newTestSetup(c client.Client) *testSetup {
	f := service.NewCPIFinder(c, projectID)
	m := &mockImageService{}
	w := mock.NewMockWaiter(c, projectID)

	// Add test-cpu-2 VM type to the fake client as it is used by many existing tests
	testVMType := &vmv1.VirtualMachineType{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cpu-2",
			Namespace: "vm-system",
		},
		Spec: vmv1.VirtualMachineTypeSpec{
			VCPUs:  2,
			Memory: resource.MustParse("2Gi"),
		},
	}
	_ = c.Create(context.Background(), testVMType)

	v := NewVMs(c, projectID, uuidGen, agentOpt, externalAccessPorts, logger, f, aes, m, w, &test.Timeout, true, mockSuccessMetadataClient, false, false, false, nil)
	vmar := &mockVMAccessChecker{}

	return &testSetup{
		c:    c,
		f:    f,
		m:    m,
		v:    v,
		w:    w,
		vmar: vmar,
	}
}

type mockMetadataClient struct{}

func (s *mockMetadataClient) Fetch(ctx context.Context, vmName string) (apiv1.AgentEnv, error) {
	return &apiv1.AgentEnvImpl{}, nil
}

func (s *mockMetadataClient) Update(ctx context.Context, vmName string, agentEnv apiv1.AgentEnv) error {
	return nil
}

type mockErrorMetadataClient struct{}

func (s *mockErrorMetadataClient) Fetch(ctx context.Context, vmName string) (apiv1.AgentEnv, error) {
	return &apiv1.AgentEnvImpl{}, errors.New("metadata fetch failure")
}

func (s *mockErrorMetadataClient) Update(ctx context.Context, vmName string, agentEnv apiv1.AgentEnv) error {
	return errors.New("metadata update failure")
}

type uuidGeneratorMock struct{}

func (u uuidGeneratorMock) Generate() (uuid string, err error) {
	return mockVMUUID, nil
}

type mockVMAccessChecker struct {
	ensureVMExistAndReadyCalled bool
	vmName                      string
	sshPublicKey                string
	zone                        string
}

func (vmar *mockVMAccessChecker) EnsureVMARExistAndReady(vmName string, sshPublicKey string) error {
	vmar.ensureVMExistAndReadyCalled = true
	vmar.vmName = vmName
	vmar.sshPublicKey = sshPublicKey
	return nil
}

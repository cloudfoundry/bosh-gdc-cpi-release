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

package disk

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
	"github.com/cloudfoundry/bosh-cpi-go/rpc"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	backupv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/backup/v1"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait/mock"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

type boshFunctionVersion int

const (
	boshFunctionVersion1 boshFunctionVersion = 1
	boshFunctionVersion2 boshFunctionVersion = 2

	mockDiskUUID     = "7ce97a29-218d-4025-751c-0aa75406f103"
	expectedDiskName = "disk-7ce97a29"

	vmIP        = "1.2.3.4"
	vmIngressIP = "5.6.7.8"
)

var (
	logger          = cfboshlog.New(boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr))
	uuidGen         = uuidGeneratorMock{}
	projectID       = "test-project"
	diskCIDComparer = cmp.Comparer(func(a, b apiv1.DiskCID) bool {
		return a.AsString() == b.AsString()
	})

	testTimeNow = time.Date(2023, 1, 1, 1, 1, 1, 1, time.UTC)
)

func TestHasDisk(t *testing.T) {
	disk := virtualMachineDisk("foo")
	c := test.FakeClient(disk)
	f := service.NewCPIFinder(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	testCases := map[string]struct {
		expected bool
		diskID   string
	}{
		"has Disk": {
			expected: true,
			diskID:   "foo",
		},
		"not has Disk": {
			expected: false,
			diskID:   "bar",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			actual, err := disks.HasDisk(apiv1.NewDiskCID(tc.diskID))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if actual != tc.expected {
				t.Fatalf("HasDisk(%s) = %t, but want %t", tc.diskID, actual, tc.expected)
			}
		})
	}
}

func TestHasDiskError(t *testing.T) {
	c := test.FakeClient()
	disks := NewDisks(c, projectID, errorObjectFinder{}, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})
	if _, err := disks.HasDisk(apiv1.NewDiskCID("foo")); err == nil {
		t.Error("got nil but expect an error")
	}
}

func TestGetDisks(t *testing.T) {
	disk1 := virtualMachineDisk("foo")
	disk2 := virtualMachineDisk("bar")
	diskVM := virtualMachine("disks", disk1, disk2)
	noDiskVM := virtualMachine("no-disks")
	c := test.FakeClient(disk1, disk2, diskVM, noDiskVM)
	f := service.NewCPIFinder(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	testCases := map[string]struct {
		vmName           string
		expectedDiskCIDs []apiv1.DiskCID
	}{
		"has disks": {
			vmName:           "disks",
			expectedDiskCIDs: []apiv1.DiskCID{apiv1.NewDiskCID("foo"), apiv1.NewDiskCID("bar")},
		},
		"does not have disks": {
			vmName:           "no-disks",
			expectedDiskCIDs: []apiv1.DiskCID{},
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			actualDiskCIDs, err := disks.GetDisks(apiv1.NewVMCID(tc.vmName))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !cmp.Equal(actualDiskCIDs, tc.expectedDiskCIDs, cmpopts.EquateEmpty(), diskCIDComparer) {
				t.Fatalf("vm has disks: %v, but should have: %v", actualDiskCIDs, tc.expectedDiskCIDs)
			}
		})
	}
}

func TestResizeDisk(t *testing.T) {
	ctx := context.Background()
	diskCID := apiv1.NewDiskCID("disk")
	initialSize := resource.MustParse("10Mi")
	disk := virtualMachineDisk(diskCID.AsString(), WithDiskSize(initialSize))
	c := test.FakeClient(disk)

	newSize := resource.MustParse("20Mi")
	f := service.NewCPIFinder(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, getWaiterWithServerSideActionForDiskSizeUpdate(c, newSize), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})
	err := disks.ResizeDisk(diskCID, 20)
	if err != nil {
		t.Fatal("got unexpected error: %w", err)
	}

	gotDisk, err := f.FindDisk(ctx, diskCID.AsString())
	if err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}
	if gotDisk.Status.Size.Cmp(newSize) != 0 {
		t.Fatalf("got: %v is not equal to want: %v", gotDisk.Status.Size, newSize)
	}

	opts := []cmp.Option{
		cmpopts.IgnoreFields(vmv1.VirtualMachineDisk{}, "Status.Size", "Spec.Size", "ObjectMeta"),
		cmpopts.IgnoreUnexported(vmv1.VirtualMachineDiskSpec{}),
	}
	if diff := cmp.Diff(gotDisk, disk, opts...); diff != "" {
		t.Fatalf("Other fields got modified, %v", diff)
	}
}

func TestResizeDiskWithAttachedVMs(t *testing.T) {
	diskCID := apiv1.NewDiskCID("disk")
	initialSize := resource.MustParse("10Mi")
	disk := virtualMachineDisk(diskCID.AsString(), WithDiskSize(initialSize))
	disk.Status.VirtualMachineAttachments = append(disk.Status.VirtualMachineAttachments, vmv1.VirtualMachineAttachment{})
	c := test.FakeClient(disk)
	f := service.NewCPIFinder(c, projectID)
	testCases := map[string]struct {
		enableHotDiskExpansion bool
		error                  bool
	}{
		"hot disk expansion not enabled": {
			error: true,
		},
		"hot disk expansion enabled": {
			enableHotDiskExpansion: true,
			error:                  false,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, tc.enableHotDiskExpansion, true, false, &statefulMockMetadataClient{})
			err := disks.ResizeDisk(diskCID, 20)

			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatal("unexpected error")
			}
			if tc.error && !strings.Contains(err.Error(), "has attached VMs") {
				t.Fatal("expected disk to have attached VMs")
			}
		})
	}
}

func TestResizeDiskWhenWaiterThrowsError(t *testing.T) {
	diskCID := apiv1.NewDiskCID("disk")
	initialSize := resource.MustParse("10Mi")
	disk := virtualMachineDisk(diskCID.AsString(), WithDiskSize(initialSize))
	c := test.FakeClient(disk)
	f := service.NewCPIFinder(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, getErroneousWaiter(c), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	err := disks.ResizeDisk(diskCID, 20)

	if err == nil {
		t.Fatal("got nil but expected an error")
	}
	if typedErr, ok := err.(rpc.CloudError); ok {
		if typedErr.Type() != "Bosh::Clouds::CloudError" {
			t.Fatalf("expected Bosh::Clouds::CloudError, got: %v", typedErr.Type())
		}
	}
}

func TestCreateDisks(t *testing.T) {
	diskSize := 10
	testCases := map[string]struct {
		size         int
		prop         Property
		wantDiskType vmv1.DiskType
		wantErr      bool
	}{
		"Standard disk type": {
			size: diskSize,
			prop: Property{
				DiskType: "Standard",
				Zone:     "us-westb",
			},
			wantDiskType: vmv1.DiskTypeStandard,
			wantErr:      false,
		},
		"Performance disk type": {
			size: diskSize,
			prop: Property{
				DiskType: "Performance",
				Zone:     "us-westb",
			},
			wantDiskType: vmv1.DiskTypePerformance,
			wantErr:      false,
		},
		"Empty disk type defaults to Standard disk type": {
			size: diskSize,
			prop: Property{
				Zone: "us-westb",
			},
			wantDiskType: vmv1.DiskTypeStandard,
			wantErr:      false,
		},
		"Unsupported disk type": {
			size: diskSize,
			prop: Property{
				DiskType: "Unsupported",
				Zone:     "us-westb",
			},
			wantErr: true,
		},
		"Disk size is zero (invalid)": {
			size: 0,
			prop: Property{
				DiskType: "Standard",
				Zone:     "us-westb",
			},
			wantDiskType: vmv1.DiskTypeStandard,
			wantErr:      true,
		},
		"Disk size is negative (invalid)": {
			size: -10,
			prop: Property{
				DiskType: "Standard",
				Zone:     "us-westb",
			},
			wantDiskType: vmv1.DiskTypeStandard,
			wantErr:      true,
		},
	}

	ctx := context.Background()
	vmCID := apiv1.NewVMCID("vm")
	vm := virtualMachine(vmCID.AsString())
	c := test.FakeClient(vm)
	f := service.NewCPIFinder(c, projectID)
	vmService := &mockVMService{client: c}
	waiter := getWaiterWithServerSideActionForDiskStateUpdate(c, vmv1.DiskPhaseSucceeded)
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			diskProp, err := convert(tc.prop)
			if err != nil {
				t.Fatalf("%v: convert(%v)=%v, want: no error.", id, tc.prop, err)
			}
			disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, waiter, vmService, &test.Timeout, false, true, false, &statefulMockMetadataClient{})
			diskCID, err := disks.CreateDisk(tc.size, diskProp, &vmCID)
			if err != nil && !tc.wantErr {
				t.Fatalf("%v: disks.CreateDisk(%v,%v,%v)=%v, want: no error.", id, tc.size, tc.prop, vmCID.AsString(), err)
			}
			if err == nil {
				t.Cleanup(func() {
					t.Logf("Cleanup disk with CID: %v", diskCID.AsString())
					disks.DeleteDisk(diskCID)
				})

				if tc.wantErr {
					t.Fatalf("%v: disks.CreateDisk(%v,%v,%v)=%v, want: error.", id, tc.size, tc.prop, vmCID.AsString(), diskCID.AsString())
				}

				disk, err := f.FindDisk(ctx, diskCID.AsString())
				if err != nil {
					t.Fatalf("%v: f.FindDisk(%v)=%v, want: no error.", id, diskCID.AsString(), err)
				}

				wantSize := resource.MustParse(fmt.Sprintf("%vMi", tc.size))
				gotSize := disk.Spec.Size
				if wantSize.Cmp(gotSize) != 0 {
					t.Errorf("%v: disks.CreateDisk(%v,%v,%v)=%v, want: %v.", id, tc.size, tc.prop, vmCID.AsString(), gotSize.String(), wantSize.String())
				}
				gotDiskType := disk.Spec.Type
				if gotDiskType != tc.wantDiskType {
					t.Errorf("%v: disks.CreateDisk(%v,%v,%v)=%v, want: %v.", id, tc.size, tc.prop, vmCID.AsString(), gotDiskType, tc.wantDiskType)
				}
			}
		})
	}
}

func convert(prop Property) (apiv1.DiskCloudProps, error) {
	jsonData, err := json.Marshal(prop)
	if err != nil {
		return nil, err
	}
	return apiv1.CloudPropsImpl{
		RawMessage: json.RawMessage(jsonData),
	}, nil
}

func TestCreateDiskFail_CleanUpSuccess(t *testing.T) {
	ctx := context.Background()
	vmCID := apiv1.NewVMCID("vm")
	vm := virtualMachine(vmCID.AsString())
	c := test.FakeClient(vm)
	f := service.NewCPIFinder(c, projectID)
	waiter := getWaiterWithServerSideActionForDiskStateUpdate(c, vmv1.DiskPhasePending)
	vmService := &mockVMService{client: c}

	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, waiter, vmService, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if _, err := disks.CreateDisk(10, apiv1.NewVMCloudPropsFromMap(map[string]interface{}{}), &vmCID); err == nil {
		t.Fatal("createdisk should report error because disk phase is not succeeded")
	}

	var results vmv1.VirtualMachineDiskList
	err := c.List(ctx, &results)
	if err != nil {
		t.Fatalf("list disks: %v", err)
	}
	if len(results.Items) != 0 {
		t.Fatal("disk should no longer exist")
	}
}

func TestDeleteDiskSuccess(t *testing.T) {
	ctx := context.Background()
	disk := virtualMachineDisk("foo")
	c := test.FakeClient(disk)
	f := service.NewCPIFinder(c, projectID)
	vmService := &mockVMService{client: c}

	waiter := mock.NewMockWaiter(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, waiter, vmService, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if err := disks.DeleteDisk(apiv1.NewDiskCID(disk.Name)); err != nil {
		t.Fatalf("deletedisk unexpected error: %v", err)
	}

	hasDisk, err := f.HasDisk(ctx, disk.Name)
	if err != nil {
		t.Fatalf("hasdisk unexpected error: %v", err)
	}
	if hasDisk {
		t.Fatal("disk should no longer exist")
	}
}

func TestDeleteDisk_DiskNotExist(t *testing.T) {
	c := test.FakeClient()
	f := service.NewCPIFinder(c, projectID)
	vmService := &mockVMService{client: c}

	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), vmService, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if err := disks.DeleteDisk(apiv1.NewDiskCID("non-existent-disk-id")); err != nil {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestAttachDisk_Variations(t *testing.T) {
	tests := []struct {
		name              string
		version           boshFunctionVersion
		vmRunning         bool
		useMetadataServer bool
		useHotPlug        bool
		wantStartCount    int
		wantStopCount     int
	}{
		{
			name:              "v1_stopped_ssh_coldplug",
			version:           boshFunctionVersion1,
			vmRunning:         false,
			useMetadataServer: false,
			useHotPlug:        false,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "v2_stopped_ssh_coldplug",
			version:           boshFunctionVersion2,
			vmRunning:         false,
			useMetadataServer: false,
			useHotPlug:        false,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "v2_stopped_ssh_hotplug",
			version:           boshFunctionVersion2,
			vmRunning:         false,
			useMetadataServer: false,
			useHotPlug:        true,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "v2_stopped_metadata_coldplug",
			version:           boshFunctionVersion2,
			vmRunning:         false,
			useMetadataServer: true,
			useHotPlug:        false,
			wantStartCount:    0,
			wantStopCount:     0,
		},
		{
			name:              "v2_stopped_metadata_hotplug",
			version:           boshFunctionVersion2,
			vmRunning:         false,
			useMetadataServer: true,
			useHotPlug:        true,
			wantStartCount:    0,
			wantStopCount:     0,
		},
		{
			name:              "v2_running_ssh_coldplug",
			version:           boshFunctionVersion2,
			vmRunning:         true,
			useMetadataServer: false,
			useHotPlug:        false,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "v2_running_ssh_hotplug",
			version:           boshFunctionVersion2,
			vmRunning:         true,
			useMetadataServer: false,
			useHotPlug:        true,
			wantStartCount:    0,
			wantStopCount:     0,
		},
		{
			name:              "v2_running_metadata_coldplug",
			version:           boshFunctionVersion2,
			vmRunning:         true,
			useMetadataServer: true,
			useHotPlug:        false,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "v2_running_metadata_hotplug",
			version:           boshFunctionVersion2,
			vmRunning:         true,
			useMetadataServer: true,
			useHotPlug:        true,
			wantStartCount:    0,
			wantStopCount:     0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testAttachDiskSuccess(t, tc.version, tc.vmRunning, tc.useMetadataServer, tc.useHotPlug, tc.wantStartCount, tc.wantStopCount)
		})
	}
}

func testAttachDiskSuccess(t *testing.T, version boshFunctionVersion, vmRunning bool, useMetadataServer bool, useHotPlug bool, wantStartCount int, wantStopCount int) {
	ctx := context.Background()
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	vm := virtualMachine(vmCID.AsString())
	vmState := vmv1.VirtualMachineStateStopped
	if vmRunning {
		vmState = vmv1.VirtualMachineStateRunning
	}
	vm.Status.State = vmState
	vmAccess := virtualMachineExternalAccess(vmCID.AsString())
	disk := virtualMachineDisk(diskCID.AsString())
	c := test.FakeClient(vm, disk, vmAccess)
	f := service.NewCPIFinder(c, projectID)
	mockAgentService := &statefulMockAgentService{}
	mockMetadataClient := &statefulMockMetadataClient{}
	mockVMSrv := &mockVMService{client: c}
	waiter := mock.NewMockWaiter(c, projectID)
	if useHotPlug {
		waiter.TargetObjects["VirtualMachine"] = vm
		waiter.ServerSideActions["VirtualMachine"] = func(ctx context.Context, cl client.Client) error {
			v := &vmv1.VirtualMachine{}
			if err := cl.Get(ctx, client.ObjectKey{Name: vm.Name, Namespace: vm.Namespace}, v); err != nil {
				return err
			}
			attachments := []vmv1.VirtualMachineAttachedDiskStatus{}
			for _, d := range v.Spec.Disks {
				hp := true
				attachments = append(attachments, vmv1.VirtualMachineAttachedDiskStatus{
					Name:         d.VirtualMachineDiskRef.Name,
					Hotpluggable: &hp,
					Phase:        vmv1.AttachedDiskPhaseReady,
				})
			}
			v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{Attachments: attachments}
			return cl.Update(ctx, v)
		}
	}
	disks := NewDisks(c, projectID, f, logger, uuidGen, mockAgentService, waiter, mockVMSrv, &test.Timeout, false, useMetadataServer, useHotPlug, mockMetadataClient)

	var hint apiv1.DiskHint
	var err error
	if version == boshFunctionVersion1 {
		err = disks.AttachDisk(vmCID, diskCID)
	} else if version == boshFunctionVersion2 {
		hint, err = disks.AttachDiskV2(vmCID, diskCID)
	}

	if err != nil {
		t.Fatalf("attachdisk: %v", err)
	}

	vm, err = f.FindVM(ctx, vmCID.AsString())
	if err != nil {
		t.Fatalf("findvm unexpected error: %v", err)
	}

	// v1 & v2 assertions
	if len(vm.Spec.Disks) != 1 {
		t.Fatalf("vm number of attached disks expected to be 1, is: %v", len(vm.Spec.Disks))
	}

	if vm.Spec.Disks[0].VirtualMachineDiskRef.Name != diskCID.AsString() {
		t.Fatalf("expected attached disk to be %v, is: %v", diskCID.AsString(), vm.Spec.Disks[0].VirtualMachineDiskRef.Name)
	}

	// v2 assertions
	if version != boshFunctionVersion2 {
		return
	}

	expectedHintJson, err := apiv1.NewDiskHintFromString("/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_disk").MarshalJSON()
	if err != nil {
		t.Fatalf("v2 assertion failed marshaling expected hint json: %v", expectedHintJson)
	}
	actualHintJson, err := hint.MarshalJSON()
	if err != nil {
		t.Fatalf("v2 assertion failed marshaling actual hint json: %v", actualHintJson)
	}
	if !cmp.Equal(expectedHintJson, actualHintJson) {
		t.Fatalf("actual disk hint json is different than expected: %v", cmp.Diff(expectedHintJson, actualHintJson))
	}

	var actualJson map[string]interface{}
	if useMetadataServer {
		actualJson, err = mockMetadataClient.getAgentEnv()
		if err != nil {
			t.Fatalf("convert actual agent env to json: %v", err)
		}
	} else {
		actualJson, err = mockAgentService.getAgentEnv()
		if err != nil {
			t.Fatalf("convert actual agent env to json: %v", err)
		}
	}

	expectedAgentConfigBytes := []byte("{ \"disks\": { \"persistent\": { \"disk\": \"/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_disk\" } } }")
	expectedAgentConfig, err := apiv1.AgentEnvFactory{}.FromBytes(expectedAgentConfigBytes)
	if err != nil {
		t.Fatalf("convert expected agent env from bytes to config: %v", err)
	}
	expectedJson, err := test.ConvertAgentEnvToJsonMap(expectedAgentConfig)
	if err != nil {
		t.Fatalf("convert expected agent env to json: %v", err)
	}

	if diff := cmp.Diff(expectedJson, actualJson); diff != "" {
		t.Fatalf("unexpected agent env; diff (-want +got):\n%s", diff)
	}

	if vm.Status.State != vmState {
		t.Fatalf("got vm state %s, want %s", vm.Status.State, vmState)
	}

	if mockVMSrv.stopCallCount != wantStopCount {
		t.Fatalf("vm service Stop called %d times (vm running: %t), want %d", mockVMSrv.stopCallCount, vmRunning, wantStopCount)
	}

	if mockVMSrv.startCallCount != wantStartCount {
		t.Fatalf("vm service Start called %d times (vm running: %t), want %d", mockVMSrv.startCallCount, vmRunning, wantStartCount)
	}
}

func TestAttachDiskV1Success_DiskAlreadyAttached(t *testing.T) {
	testAttachDiskSuccess_DiskAlreadyAttached(t, boshFunctionVersion1)
}

func TestAttachDiskV2Success_DiskAlreadyAttached(t *testing.T) {
	testAttachDiskSuccess_DiskAlreadyAttached(t, boshFunctionVersion2)
}

func testAttachDiskSuccess_DiskAlreadyAttached(t *testing.T, version boshFunctionVersion) {
	ctx := context.Background()
	diskCID := apiv1.NewDiskCID("disk")
	disk := virtualMachineDisk(diskCID.AsString())
	vmCID := apiv1.NewVMCID("vm")
	vm := virtualMachine(vmCID.AsString(), disk)
	vm.Status.State = vmv1.VirtualMachineStateRunning
	c := test.FakeClient(vm, disk)
	f := service.NewCPIFinder(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	var err error
	if version == boshFunctionVersion1 {
		err = disks.AttachDisk(vmCID, diskCID)
	} else if version == boshFunctionVersion2 {
		_, err = disks.AttachDiskV2(vmCID, diskCID)
	}

	if err != nil {
		t.Fatalf("attachdisk: %v", err)
	}

	vm, err = f.FindVM(ctx, vmCID.AsString())
	if err != nil {
		t.Fatalf("findvm unexpected error: %v", err)
	}

	// v1 & v2 assertions
	if len(vm.Spec.Disks) != 1 {
		t.Fatalf("vm number of attached disks expected to be 1, is: %v", len(vm.Spec.Disks))
	}

	if vm.Spec.Disks[0].VirtualMachineDiskRef.Name != diskCID.AsString() {
		t.Fatalf("expected attached disk to be %v, is: %v", diskCID.AsString(), vm.Spec.Disks[0].VirtualMachineDiskRef.Name)
	}
}

func TestAttachDiskFailed(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	vm := virtualMachine(vmCID.AsString())
	disk := virtualMachineDisk(diskCID.AsString())
	c := test.FakeClient(vm, disk)
	disks := NewDisks(c, projectID, errorObjectFinder{}, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if err := disks.AttachDisk(vmCID, diskCID); err == nil {
		t.Fatal("got nil but expect an error")
	}
}

func TestAttachDiskV2Failed(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	vm := virtualMachine(vmCID.AsString())
	disk := virtualMachineDisk(diskCID.AsString())
	c := test.FakeClient(vm, disk)
	disks := NewDisks(c, projectID, errorObjectFinder{}, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if _, err := disks.AttachDiskV2(vmCID, diskCID); err == nil {
		t.Fatal("got nil but expect an error")
	}
}

func TestAttachDiskV2Failed_InvalidVMState(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	vm := virtualMachine(vmCID.AsString())
	vm.Status.State = vmv1.VirtualMachineStatePendingIPAllocation // Invalid state
	disk := virtualMachineDisk(diskCID.AsString())
	c := test.FakeClient(vm, disk)
	f := service.NewCPIFinder(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	_, err := disks.AttachDiskV2(vmCID, diskCID)
	if err == nil {
		t.Fatal("got nil but expect an error")
	}

	expectedErr := fmt.Sprintf("VM %s (State: %s) is not in a valid state (running or stopped) to attach disk %s", vmCID.AsString(), vm.Status.State, diskCID.AsString())
	if !strings.Contains(err.Error(), expectedErr) {
		t.Fatalf("expected error to contain %q, but got %v", expectedErr, err)
	}
}

func TestAttachDiskV2Failed_StopVMFailed(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	vm := virtualMachine(vmCID.AsString())
	vm.Status.State = vmv1.VirtualMachineStateRunning
	disk := virtualMachineDisk(diskCID.AsString())
	c := test.FakeClient(vm, disk)
	mockVMSrv := &mockVMService{stopVMFailed: true}
	disks := NewDisks(c, projectID, errorObjectFinder{}, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), mockVMSrv, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if _, err := disks.AttachDiskV2(vmCID, diskCID); err == nil {
		t.Fatal("got nil but expect an error")
	}
}

func TestAttachDiskV2_FailedAgentEnvFetchUsingSSH(t *testing.T) {
	testAttachDiskV2_FailedAgentEnvFetch(t, false /* Use Agent Service */)
}

func TestAttachDiskV2_FailedAgentEnvFetchUsingMetadataserver(t *testing.T) {
	testAttachDiskV2_FailedAgentEnvFetch(t, true /* Use Metadata Server */)
}

func testAttachDiskV2_FailedAgentEnvFetch(t *testing.T, useMetadataServer bool) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")

	vm := virtualMachine(vmCID.AsString())
	vm.Status.State = vmv1.VirtualMachineStateRunning
	disk := virtualMachineDisk(diskCID.AsString())
	vmAccess := virtualMachineExternalAccess(vmCID.AsString())

	c := test.FakeClient(vm, disk, vmAccess)
	f := service.NewCPIFinder(c, projectID)
	mockVMSrv := &mockVMService{client: c}
	var mockAgentSvc agentClient
	var mockMetadataClient metadataClient
	if useMetadataServer {
		mockAgentSvc = &statefulMockAgentService{}
		mockMetadataClient = &mockErrorMetadataClient{}
	} else {
		mockAgentSvc = &mockErrorAgentService{}
		mockMetadataClient = &statefulMockMetadataClient{}
	}

	disks := NewDisks(c, projectID, f, logger, uuidGen, mockAgentSvc, mock.NewMockWaiter(c, projectID), mockVMSrv, &test.Timeout, false, useMetadataServer, false, mockMetadataClient)
	_, err := disks.AttachDiskV2(vmCID, diskCID)
	expectedError := "fetch agent env: agent fetch failure"

	if err == nil {
		t.Fatal("AttachDiskV2 got nil, expected error")
	}
	if !strings.Contains(err.Error(), expectedError) {
		t.Errorf("AttachDiskV2 error message mismatch: got %q, want %q", err.Error(), expectedError)
	}
}

func TestAttachDiskV2_FailedAgentEnvUpdateUsingSSH(t *testing.T) {
	testAttachDiskV2_FailedAgentEnvUpdate(t, false /* Use Agent Service */)
}

func TestAttachDiskV2_FailedAgentEnvUpdateUsingMetadataserver(t *testing.T) {
	testAttachDiskV2_FailedAgentEnvUpdate(t, true /* Use Metadata Server */)
}

func testAttachDiskV2_FailedAgentEnvUpdate(t *testing.T, useMetadataServer bool) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")

	vm := virtualMachine(vmCID.AsString())
	vm.Status.State = vmv1.VirtualMachineStateRunning
	disk := virtualMachineDisk(diskCID.AsString())
	vmAccess := virtualMachineExternalAccess(vmCID.AsString())

	c := test.FakeClient(vm, disk, vmAccess)
	f := service.NewCPIFinder(c, projectID)
	mockVMSrv := &mockVMService{client: c}
	var mockAgentSvc agentClient
	var mockMetadataClient metadataClient
	if useMetadataServer {
		mockAgentSvc = &statefulMockAgentService{}
		mockMetadataClient = &mockUpdateErrorMetadataClient{}
	} else {
		mockAgentSvc = &mockUpdateErrorAgentService{}
		mockMetadataClient = &statefulMockMetadataClient{}
	}

	disks := NewDisks(c, projectID, f, logger, uuidGen, mockAgentSvc, mock.NewMockWaiter(c, projectID), mockVMSrv, &test.Timeout, false, useMetadataServer, false, mockMetadataClient)

	_, err := disks.AttachDiskV2(vmCID, diskCID)
	expectedError := "update agent env: agent update failure"

	if err == nil {
		t.Fatal("AttachDiskV2 got nil, expected error")
	}
	if !strings.Contains(err.Error(), expectedError) {
		t.Errorf("AttachDiskV2 error message mismatch: got %q, want %q", err.Error(), expectedError)
	}
}

func TestDetachDisk_FailedAgentEnvFetchUsingSSH(t *testing.T) {
	testDetachDisk_FailedAgentEnvFetch(t, false /* Use Agent Service */)
}

func TestDetachDisk_FailedAgentEnvFetchUsingMetadataServer(t *testing.T) {
	testDetachDisk_FailedAgentEnvFetch(t, true /* Use Metadata Server */)
}

func testDetachDisk_FailedAgentEnvFetch(t *testing.T, useMetadataServer bool) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")

	diskToDetach := virtualMachineDisk(diskCID.AsString())
	vm := virtualMachine(vmCID.AsString(), diskToDetach)
	vm.Status.State = vmv1.VirtualMachineStateRunning

	vmAccess := virtualMachineExternalAccess(vmCID.AsString())
	c := test.FakeClient(vm, diskToDetach, vmAccess)
	f := service.NewCPIFinder(c, projectID)
	mockVMSrv := &mockVMService{client: c}
	var mockAgentSvc agentClient
	var mockMetadataClient metadataClient
	if useMetadataServer {
		mockAgentSvc = &statefulMockAgentService{}
		mockMetadataClient = &mockErrorMetadataClient{}
	} else {
		mockAgentSvc = &mockErrorAgentService{}
		mockMetadataClient = &statefulMockMetadataClient{}
	}

	disks := NewDisks(c, projectID, f, logger, uuidGen, mockAgentSvc, mock.NewMockWaiter(c, projectID), mockVMSrv, &test.Timeout, false, useMetadataServer, false, mockMetadataClient)

	err := disks.DetachDisk(vmCID, diskCID)

	if err == nil {
		t.Fatal("DetachDisk got nil, expected error")
	}

	expectedError := "fetch agent env: agent fetch failure"
	if !strings.Contains(err.Error(), expectedError) {
		t.Errorf("DetachDisk error message mismatch: got %q, want to contain %q", err.Error(), expectedError)
	}
}

func TestDetachDisk_FailedAgentEnvUpdateUsingSSH(t *testing.T) {
	testDetachDisk_FailedAgentEnvUpdate(t, false /* Use Agent Service */)
}

func TestDetachDisk_FailedAgentEnvUpdateUsingMetadataServer(t *testing.T) {
	testDetachDisk_FailedAgentEnvUpdate(t, true /* Use Metadata Server */)
}

func testDetachDisk_FailedAgentEnvUpdate(t *testing.T, useMetadataServer bool) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")

	diskToDetach := virtualMachineDisk(diskCID.AsString())
	vm := virtualMachine(vmCID.AsString(), diskToDetach)
	vm.Status.State = vmv1.VirtualMachineStateRunning

	vmAccess := virtualMachineExternalAccess(vmCID.AsString())
	c := test.FakeClient(vm, diskToDetach, vmAccess)
	f := service.NewCPIFinder(c, projectID)
	mockVMSrv := &mockVMService{client: c}
	var mockAgentSvc agentClient
	var mockMetadataClient metadataClient
	if useMetadataServer {
		mockAgentSvc = &statefulMockAgentService{}
		mockMetadataClient = &mockUpdateErrorMetadataClient{}
	} else {
		mockAgentSvc = &mockUpdateErrorAgentService{}
		mockMetadataClient = &statefulMockMetadataClient{}
	}

	disks := NewDisks(c, projectID, f, logger, uuidGen, mockAgentSvc, mock.NewMockWaiter(c, projectID), mockVMSrv, &test.Timeout, false, useMetadataServer, false, mockMetadataClient)

	err := disks.DetachDisk(vmCID, diskCID)

	if err == nil {
		t.Fatal("DetachDisk got nil, expected error")
	}

	expectedError := "update agent env: agent update failure"
	if !strings.Contains(err.Error(), expectedError) {
		t.Errorf("DetachDisk error message mismatch: got %q, want to contain %q", err.Error(), expectedError)
	}
}

func TestDetachDisk_Variations(t *testing.T) {
	tests := []struct {
		name              string
		vmRunning         bool
		useMetadataServer bool
		useHotPlug        bool
		wantStartCount    int
		wantStopCount     int
	}{
		{
			name:              "stopped_ssh_coldplug",
			vmRunning:         false,
			useMetadataServer: false,
			useHotPlug:        false,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "stopped_ssh_hotplug",
			vmRunning:         false,
			useMetadataServer: false,
			useHotPlug:        true,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "stopped_metadata_coldplug",
			vmRunning:         false,
			useMetadataServer: true,
			useHotPlug:        false,
			wantStartCount:    0,
			wantStopCount:     0,
		},
		{
			name:              "stopped_metadata_hotplug",
			vmRunning:         false,
			useMetadataServer: true,
			useHotPlug:        true,
			wantStartCount:    0,
			wantStopCount:     0,
		},
		{
			name:              "running_ssh_coldplug",
			vmRunning:         true,
			useMetadataServer: false,
			useHotPlug:        false,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "running_ssh_hotplug",
			vmRunning:         true,
			useMetadataServer: false,
			useHotPlug:        true,
			wantStartCount:    0,
			wantStopCount:     0,
		},
		{
			name:              "running_metadata_coldplug",
			vmRunning:         true,
			useMetadataServer: true,
			useHotPlug:        false,
			wantStartCount:    1,
			wantStopCount:     1,
		},
		{
			name:              "running_metadata_hot",
			vmRunning:         true,
			useMetadataServer: true,
			useHotPlug:        true,
			wantStartCount:    0,
			wantStopCount:     0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testDetachDiskSuccess(t, tc.vmRunning, tc.useMetadataServer, tc.useHotPlug, tc.wantStartCount, tc.wantStopCount)
		})
	}
}

func testDetachDiskSuccess(t *testing.T, vmRunning bool, useMetadataServer bool, useHotPlug bool, wantStartCount int, wantStopCount int) {
	ctx := context.Background()
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	disk := virtualMachineDisk(diskCID.AsString())
	detachedDiskCID := apiv1.NewDiskCID("detached-disk")
	detachedDisk := virtualMachineDisk(detachedDiskCID.AsString())
	vm := virtualMachine(vmCID.AsString(), disk, detachedDisk)
	vmState := vmv1.VirtualMachineStateStopped
	if vmRunning {
		vmState = vmv1.VirtualMachineStateRunning
	}
	vm.Status.State = vmState
	vmAccess := virtualMachineExternalAccess(vmCID.AsString())
	c := test.FakeClient(vm, detachedDisk, vmAccess)
	f := service.NewCPIFinder(c, projectID)
	mockVMSrv := &mockVMService{client: c}
	mockAgentService := &statefulMockAgentService{}
	mockMetadataClient := &statefulMockMetadataClient{}
	waiter := mock.NewMockWaiter(c, projectID)
	if useHotPlug {
		waiter.TargetObjects["VirtualMachine"] = vm
		waiter.ServerSideActions["VirtualMachine"] = func(ctx context.Context, cl client.Client) error {
			v := &vmv1.VirtualMachine{}
			if err := cl.Get(ctx, client.ObjectKey{Name: vm.Name, Namespace: vm.Namespace}, v); err != nil {
				return err
			}
			attachments := []vmv1.VirtualMachineAttachedDiskStatus{}
			for _, d := range v.Spec.Disks {
				hp := true
				attachments = append(attachments, vmv1.VirtualMachineAttachedDiskStatus{
					Name:         d.VirtualMachineDiskRef.Name,
					Hotpluggable: &hp,
					Phase:        vmv1.AttachedDiskPhaseReady,
				})
			}
			v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{Attachments: attachments}
			return cl.Update(ctx, v)
		}
	}
	disks := NewDisks(c, projectID, f, logger, uuidGen, mockAgentService, waiter, mockVMSrv, &test.Timeout, false, useMetadataServer, useHotPlug, mockMetadataClient)

	if len(vm.Spec.Disks) != 2 {
		t.Fatalf("vm number of attached disks expected to be 2, is: %v", len(vm.Spec.Disks))
	}

	if err := disks.DetachDisk(vmCID, detachedDiskCID); err != nil {
		t.Fatalf("detachdisk failed: %v", err)
	}

	actualVM, err := f.FindVM(ctx, vmCID.AsString())
	if err != nil {
		t.Fatalf("findvm unexpected error: %v", err)
	}

	if len(actualVM.Spec.Disks) != 1 {
		t.Fatalf("vm number of attached disks expected to be 1, is: %v", len(actualVM.Spec.Disks))
	}

	if vm.Spec.Disks[0].VirtualMachineDiskRef.Name != diskCID.AsString() {
		t.Fatalf("expected disk %v to still be attached, but it is not.", diskCID.AsString())
	}

	var actualJson map[string]interface{}
	if useMetadataServer {
		actualJson, err = mockMetadataClient.getAgentEnv()
		if err != nil {
			t.Fatalf("convert actual agent env to json: %v", err)
		}
	} else {
		actualJson, err = mockAgentService.getAgentEnv()
		if err != nil {
			t.Fatalf("convert actual agent env to json: %v", err)
		}
	}
	expectedAgentConfigBytes := []byte("{ \"disks\": { \"persistent\": { } } }")
	expectedAgentConfig, err := apiv1.AgentEnvFactory{}.FromBytes(expectedAgentConfigBytes)
	if err != nil {
		t.Fatalf("convert expected agent env from bytes to config: %v", err)
	}
	expectedJson, err := test.ConvertAgentEnvToJsonMap(expectedAgentConfig)
	if err != nil {
		t.Fatalf("convert expected agent env to json: %v", err)
	}

	if diff := cmp.Diff(expectedJson, actualJson); diff != "" {
		t.Fatalf("unexpected agent env; diff (-want +got):\n%s", diff)
	}

	if mockVMSrv.stopCallCount != wantStopCount {
		t.Fatalf("vm service Stop called %d times (vm running: %t), want %d", mockVMSrv.stopCallCount, vmRunning, wantStopCount)
	}

	if mockVMSrv.startCallCount != wantStartCount {
		t.Fatalf("vm service Start called %d times (vm running: %t), want %d", mockVMSrv.startCallCount, vmRunning, wantStartCount)
	}
}

func TestDetacDiskFailed_StopVMFailed(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	vm := virtualMachine(vmCID.AsString())
	vm.Status.State = vmv1.VirtualMachineStateRunning
	disk := virtualMachineDisk(diskCID.AsString())
	c := test.FakeClient(vm, disk)
	mockVMSrv := &mockVMService{stopVMFailed: true}
	disks := NewDisks(c, projectID, errorObjectFinder{}, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), mockVMSrv, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if err := disks.DetachDisk(vmCID, diskCID); err == nil {
		t.Fatal("detachdisk got nil, expect error")
	}
}

func TestDetachDiskSuccess_AlreadyDetachedDisk(t *testing.T) {
	ctx := context.Background()
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	disk := virtualMachineDisk(diskCID.AsString())
	alreadyDetachedDiskCID := apiv1.NewDiskCID("detached-disk")
	alreadyDetachedDisk := virtualMachineDisk(alreadyDetachedDiskCID.AsString())
	vm := virtualMachine(vmCID.AsString(), disk)
	vm.Status.State = vmv1.VirtualMachineStateRunning
	c := test.FakeClient(vm, alreadyDetachedDisk)
	f := service.NewCPIFinder(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if len(vm.Spec.Disks) != 1 {
		t.Fatalf("vm number of attached disks expected to be 1, is: %v", len(vm.Spec.Disks))
	}

	if err := disks.DetachDisk(vmCID, alreadyDetachedDiskCID); err != nil {
		t.Fatalf("detachdisk failed: %v", err)
	}

	vm, err := f.FindVM(ctx, vmCID.AsString())
	if err != nil {
		t.Fatalf("findvm unexpected error: %v", err)
	}

	if len(vm.Spec.Disks) != 1 {
		t.Fatalf("vm number of attached disks expected to be 1, is: %v", len(vm.Spec.Disks))
	}

	if vm.Spec.Disks[0].VirtualMachineDiskRef.Name != diskCID.AsString() {
		t.Fatalf("expected disk %v to still be attached, but it is not.", diskCID.AsString())
	}
}

func TestDetachDiskFailed(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	vm := virtualMachine(vmCID.AsString())
	disk := virtualMachineDisk(diskCID.AsString())
	c := test.FakeClient(vm, disk)
	disks := NewDisks(c, projectID, errorObjectFinder{}, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if err := disks.DetachDisk(vmCID, diskCID); err == nil {
		t.Fatal("got nil but expect an error")
	}
}

func TestDetachDiskFailed_InvalidVMState(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")
	vm := virtualMachine(vmCID.AsString())
	vm.Status.State = vmv1.VirtualMachineStateStarting // Invalid state
	disk := virtualMachineDisk(diskCID.AsString())
	c := test.FakeClient(vm, disk)
	f := service.NewCPIFinder(c, projectID)
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	err := disks.DetachDisk(vmCID, diskCID)
	if err == nil {
		t.Fatal("got nil but expect an error")
	}

	expectedErr := fmt.Sprintf("VM %s (State: %s) is not in a valid state (running or stopped) to detach disk %s", vmCID.AsString(), vm.Status.State, diskCID.AsString())
	if !strings.Contains(err.Error(), expectedErr) {
		t.Fatalf("expected error to contain %q, but got %v", expectedErr, err)
	}
}

func TestSetDiskMetadataSuccess(t *testing.T) {
	diskID := "disk"
	longStr := "abvxcnbhteafuximxetbxvuyddcghlnqmkwlvebchkfuyyuybbaepbzgpdcposlnz"

	testCases := map[string]struct {
		disk     *vmv1.VirtualMachineDisk
		metadata map[string]interface{}
		expected map[string]string
	}{
		"SetMetadata on disk without labels": {
			disk: virtualMachineDisk(
				diskID,
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
		"SetMetadata on disk with labels": {
			disk: virtualMachineDisk(diskID, withLabels(map[string]string{
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
		"SetMetadata on disk with labels to overwrite": {
			disk: virtualMachineDisk(diskID, withLabels(map[string]string{
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
		"SetMetadata on disk with the same labels": {
			disk: virtualMachineDisk(diskID, withLabels(map[string]string{
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
			disk: virtualMachineDisk(diskID, withLabels(nil)),
			metadata: map[string]interface{}{
				"attached_at":   "2024-01-04T16:03:38Z",
				"instance_name": "redis/ce7d2040",
			},
			expected: map[string]string{
				"attached_at":   "20240104T160338Z",
				"instance_name": "redis--ce7d2040",
			},
		},
		"SetMetadata trims values with length violation": {
			disk: virtualMachineDisk(diskID, withLabels(nil)),
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
			c := test.FakeClient(tc.disk)
			f := service.NewCPIFinder(c, projectID)
			disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

			if err := disks.SetDiskMetadata(apiv1.NewDiskCID(diskID), apiv1.NewDiskMeta(tc.metadata)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			updatedDisk, err := f.FindDisk(context.Background(), diskID)
			if err != nil {
				t.Fatalf("find disk: %v", err)
			}

			if diff := cmp.Diff(tc.expected, updatedDisk.ObjectMeta.Labels); diff != "" {
				t.Errorf("test requests diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetDiskMetadataSuccessWithSetInAnnotations(t *testing.T) {
	diskID := "target-disk"
	longStr := "abvxcnbhteafuximxetbxvuyddcghlnqmkwlvebchkfuyyuybbaepbzgpdcposlnz"
	expectedLongStr := longStr[0:63]

	testCases := map[string]struct {
		disk                *vmv1.VirtualMachineDisk
		metadata            map[string]interface{}
		expectedLables      map[string]string
		expectedAnnotations map[string]string
	}{
		"SetMetadata on disk without annotations - no length violation": {
			disk: virtualMachineDisk(
				diskID,
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
		"SetMetadata on disk with annotations - no length vilation": {
			disk: virtualMachineDisk(diskID, withAnnotations(map[string]string{
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
		"SetMetadata on disk with annotations - with length violation": {
			disk: virtualMachineDisk(diskID, withAnnotations(map[string]string{
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
		"SetMetadata on disk with the same annotations - with length violation": {
			disk: virtualMachineDisk(diskID, withAnnotations(map[string]string{
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
		"SetMetadata on disk with annotations - overwrite with length violation": {
			disk: virtualMachineDisk(diskID, withAnnotations(map[string]string{
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
			c := test.FakeClient(tc.disk)
			f := service.NewCPIFinder(c, projectID)
			disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

			if err := disks.SetDiskMetadata(apiv1.NewDiskCID(diskID), apiv1.NewDiskMeta(tc.metadata)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			updatedDisk, err := f.FindDisk(context.Background(), diskID)
			if err != nil {
				t.Fatalf("find disk: %v", err)
			}

			if diff := cmp.Diff(tc.expectedLables, updatedDisk.ObjectMeta.Labels); diff != "" {
				t.Fatalf("test labels diff (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.expectedAnnotations, updatedDisk.ObjectMeta.Annotations); diff != "" {
				t.Fatalf("test annotations diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetVMMetadataFailed(t *testing.T) {
	c := test.FakeClient()
	disks := NewDisks(c, projectID, errorObjectFinder{}, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})
	if err := disks.SetDiskMetadata(apiv1.NewDiskCID(test.WantedObjName), apiv1.NewDiskMeta(nil)); err == nil {
		t.Fatal("got nil but expect an error")
	}
}

func TestSnapshotDiskSuccess(t *testing.T) {
	diskName := "disk-test"
	disk := virtualMachineDisk(diskName)
	c := test.FakeClient(disk)
	f := service.NewCPIFinder(c, projectID)

	testClock := clock.NewFakePassiveClock(testTimeNow)
	backupName := fmt.Sprintf("backup-%s-%d", diskName, testClock.Now().Unix())
	waiter := getWaiterWithServerSideActionForBackupUpdate(c, backupName, backupv1.BackupStateSucceeded)

	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, waiter, &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})
	disks.clock = testClock

	backupID, err := disks.SnapshotDisk(apiv1.NewDiskCID(diskName), apiv1.DiskMeta{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if backupID.AsString() != backupName {
		t.Fatalf("got backup name %s, expected %s", backupID, backupName)
	}
}

func TestSnapshotDiskFailed_DiskNotExists(t *testing.T) {
	diskName := "disk-test"
	backupName := fmt.Sprintf("%s-backup", diskName)

	backup := diskBackup(backupName, backupv1.BackupStateSucceeded)
	c := test.FakeClient(backup)
	f := service.NewCPIFinder(c, projectID)

	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	_, err := disks.SnapshotDisk(apiv1.NewDiskCID(diskName), apiv1.DiskMeta{})
	if err == nil {
		t.Fatal("got nil, expected error")
	}
}

func TestSnapshotDiskSuccess_SnapshotNotReady(t *testing.T) {
	diskName := "disk-test"
	disk := virtualMachineDisk(diskName)
	c := test.FakeClient(disk)
	f := service.NewCPIFinder(c, projectID)

	backupName := fmt.Sprintf("%s-backup", diskName)
	waiter := getWaiterWithServerSideActionForBackupUpdate(c, backupName, backupv1.BackupStateDeletedFailed)

	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, waiter, &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	_, err := disks.SnapshotDisk(apiv1.NewDiskCID(diskName), apiv1.DiskMeta{})
	if err == nil {
		t.Fatal("got nil, expected error")
	}
}

func TestSnapshotDiskFailed_BackupStateFailed(t *testing.T) {
	diskName := "disk-test"
	disk := virtualMachineDisk(diskName)
	c := test.FakeClient(disk)
	f := service.NewCPIFinder(c, projectID)

	testClock := clock.NewFakePassiveClock(testTimeNow)
	backupName := fmt.Sprintf("backup-%s-%d", diskName, testClock.Now().Unix())
	waiter := getWaiterWithServerSideActionForBackupUpdate(c, backupName, backupv1.BackupStateFailed)

	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, waiter, &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})
	disks.clock = testClock

	_, err := disks.SnapshotDisk(apiv1.NewDiskCID(diskName), apiv1.DiskMeta{})
	if err == nil {
		t.Fatal("got nil, expected error")
	}
	expectedErr := fmt.Sprintf("backup %s failed", backupName)
	if !strings.Contains(err.Error(), expectedErr) {
		t.Fatalf("expected error containing %q, got %v", expectedErr, err)
	}
}

func TestDeleteSnapshot_Success(t *testing.T) {
	diskName := "disk-test"
	backupName := fmt.Sprintf("%s-backup", diskName)

	backup := diskBackup(backupName, backupv1.BackupStateSucceeded)
	c := test.FakeClient(backup)
	f := service.NewCPIFinder(c, projectID)

	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if err := disks.DeleteSnapshot(apiv1.NewSnapshotCID(backupName)); err != nil {
		t.Fatalf("got err: %v", err)
	}

	var results vmv1.VirtualMachineDeleteBackupRequestList
	err := c.List(context.Background(), &results)
	if err != nil {
		t.Fatalf("list backup delete requests: %v", err)
	}
	if len(results.Items) == 0 {
		t.Fatal("cannot find backup delete request")
	}
}

func TestDeleteSnapshot_BackupkNotExists(t *testing.T) {
	diskName := "disk-test"
	backupName := fmt.Sprintf("%s-backup", diskName)

	c := test.FakeClient()
	f := service.NewCPIFinder(c, projectID)

	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	if err := disks.DeleteSnapshot(apiv1.NewSnapshotCID(backupName)); err != nil {
		t.Fatalf("got error: %v", err)
	}
}

func TestFindVMIP_GetVMIngress_Success(t *testing.T) {
	c := test.FakeClient()

	disks := NewDisks(c, projectID, objectFinderForVMIP{}, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	ip, err := disks.findVMIP(context.Background(), "vmtest")

	if err != nil {
		t.Fatalf("got error: %v", err)
	}

	if ip != vmIngressIP {
		t.Fatalf("got IP %s, expected vmIngressIP %s", ip, vmIngressIP)
	}
}

func TestFindVMIP_GetVMIngressFailed(t *testing.T) {
	c := test.FakeClient()

	f := objectFinderForVMIP{
		findIngressIPErr: errors.New("error"),
	}
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	_, err := disks.findVMIP(context.Background(), "vmtest")

	if err == nil {
		t.Fatal("got nil, expect error")
	}
}

func TestFindVMIP_NOVMIngress_GetVMIP_Success(t *testing.T) {
	c := test.FakeClient()

	f := objectFinderForVMIP{
		findIngressIPErr: apierrors.NewNotFound(schema.GroupResource{}, ""),
	}
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	ip, err := disks.findVMIP(context.Background(), "vmtest")

	if err != nil {
		t.Fatalf("got error: %v", err)
	}

	if ip != vmIP {
		t.Fatalf("got IP %s, expected vmIP %s", ip, vmIngressIP)
	}
}

func TestFindVMIP_NOVMIngress_GetVMIP_Failed(t *testing.T) {
	c := test.FakeClient()

	f := objectFinderForVMIP{
		findIngressIPErr: apierrors.NewNotFound(schema.GroupResource{}, ""),
		findIPErr:        errors.New("error"),
	}
	disks := NewDisks(c, projectID, f, logger, uuidGen, &statefulMockAgentService{}, mock.NewMockWaiter(c, projectID), &mockVMService{}, &test.Timeout, false, true, false, &statefulMockMetadataClient{})

	_, err := disks.findVMIP(context.Background(), "vmtest")

	if err == nil {
		t.Fatal("got nil, expect error")
	}
}

func virtualMachine(name string, disks ...*vmv1.VirtualMachineDisk) *vmv1.VirtualMachine {
	diskAttachments := make([]vmv1.DiskAttachment, len(disks))
	for i, disk := range disks {
		diskAttachments[i] = vmv1.DiskAttachment{
			VirtualMachineDiskRef: v1.LocalObjectReference{Name: disk.Name},
		}
	}

	return &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: projectID,
		},
		Spec: vmv1.VirtualMachineSpec{
			Disks: diskAttachments,
		},
	}
}

type diskOption func(*vmv1.VirtualMachineDisk)

func withLabels(labels map[string]string) diskOption {
	return func(disk *vmv1.VirtualMachineDisk) {
		disk.ObjectMeta.Labels = labels
	}
}

func withAnnotations(annotations map[string]string) diskOption {
	return func(disk *vmv1.VirtualMachineDisk) {
		disk.ObjectMeta.Annotations = annotations
	}
}

func WithDiskSize(size resource.Quantity) diskOption {
	return func(disk *vmv1.VirtualMachineDisk) {
		disk.Status.Size = size
		disk.Spec.Size = size
	}
}

func virtualMachineDisk(name string, opts ...diskOption) *vmv1.VirtualMachineDisk {
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: projectID,
		},
	}
	for _, apply := range opts {
		apply(disk)
	}
	return disk
}

func virtualMachineExternalAccess(vmName string) *vmv1.VirtualMachineExternalAccess {
	return &vmv1.VirtualMachineExternalAccess{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: projectID,
		},

		Status: vmv1.VirtualMachineExternalAccessStatus{
			IngressIP: "1.2.3.4",
		},
	}
}

func getWaiterWithServerSideActionForDiskStateUpdate(c client.Client, diskState vmv1.VirtualMachineDiskPhase) *mock.MockWaiter {
	waiter := mock.NewMockWaiter(c, projectID)

	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      expectedDiskName,
			Namespace: projectID,
		}
		disk := &vmv1.VirtualMachineDisk{}
		if err := c.Get(ctx, key, disk); err != nil {
			return fmt.Errorf("get disk: %v", err)
		}

		disk.Status.Phase = diskState
		if err := c.Update(ctx, disk); err != nil {
			return fmt.Errorf("update disk: %v", err)
		}
		return nil
	}
	waiter.ServerSideActions["VirtualMachineDisk"] = action
	waiter.TargetObjects["VirtualMachineDisk"] = &vmv1.VirtualMachineDisk{}
	return waiter
}

func getWaiterWithServerSideActionForDiskSizeUpdate(c client.Client, diskSize resource.Quantity) *mock.MockWaiter {
	waiter := mock.NewMockWaiter(c, projectID)

	action := func(ctx context.Context, c client.Client) error {
		key := client.ObjectKey{
			Name:      "disk",
			Namespace: projectID,
		}
		disk := &vmv1.VirtualMachineDisk{}
		if err := c.Get(ctx, key, disk); err != nil {
			return fmt.Errorf("get disk: %v", err)
		}

		disk.Status.Size = diskSize
		if err := c.Update(ctx, disk); err != nil {
			return fmt.Errorf("update disk: %v", err)
		}
		return nil
	}
	waiter.ServerSideActions["VirtualMachineDisk"] = action
	waiter.TargetObjects["VirtualMachineDisk"] = &vmv1.VirtualMachineDisk{}
	return waiter
}

func getErroneousWaiter(c client.Client) *mock.MockWaiter {
	waiter := mock.NewMockWaiter(c, projectID)
	err := errors.New("error")
	action := func(ctx context.Context, c client.Client) error {
		return err
	}
	waiter.ServerSideActions["VirtualMachineDisk"] = action
	waiter.TargetObjects["VirtualMachineDisk"] = &vmv1.VirtualMachineDisk{}
	return waiter
}

func getWaiterWithServerSideActionForBackupUpdate(c client.Client, backupName string, backupState backupv1.BackupState) *mock.MockWaiter {
	waiter := mock.NewMockWaiter(c, projectID)

	action := func(ctx context.Context, c client.Client) error {
		backup := diskBackup(backupName, backupState)
		if err := c.Create(ctx, backup); err != nil {
			return fmt.Errorf("create backup: %v", err)
		}
		return nil
	}
	waiter.ServerSideActions["VirtualMachineBackup"] = action
	waiter.TargetObjects["VirtualMachineBackup"] = &vmv1.VirtualMachineBackup{}
	return waiter
}

func diskBackup(backupName string, backupState backupv1.BackupState) *vmv1.VirtualMachineBackup {
	return &vmv1.VirtualMachineBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupName,
			Namespace: projectID,
		},
		Status: vmv1.VirtualMachineBackupStatus{
			BackupStatus: backupv1.BackupStatus{
				State: backupState,
			},
		},
	}
}

// A mock implementation of `objectFinder` that always returns an error.
type errorObjectFinder struct{}

func (e errorObjectFinder) HasDisk(ctx context.Context, vmID string) (bool, error) {
	return false, errors.New("failed")
}
func (e errorObjectFinder) FindDisk(ctx context.Context, diskID string) (*vmv1.VirtualMachineDisk, error) {
	return nil, errors.New("failed")
}
func (e errorObjectFinder) FindVM(ctx context.Context, vmID string) (*vmv1.VirtualMachine, error) {
	return nil, errors.New("failed")
}
func (e errorObjectFinder) FindVMIngressIP(ctx context.Context, vmID string) (string, error) {
	return "", errors.New("failed")
}

func (e errorObjectFinder) FindVMIP(ctx context.Context, vmID string) (string, error) {
	return "", errors.New("failed")
}

// A mock for findVMIP test
type objectFinderForVMIP struct {
	findIngressIPErr error
	findIPErr        error
}

func (o objectFinderForVMIP) HasDisk(ctx context.Context, vmID string) (bool, error) {
	return true, nil
}
func (o objectFinderForVMIP) FindDisk(ctx context.Context, diskID string) (*vmv1.VirtualMachineDisk, error) {
	return nil, nil
}
func (o objectFinderForVMIP) FindVM(ctx context.Context, vmID string) (*vmv1.VirtualMachine, error) {
	return nil, nil
}
func (o objectFinderForVMIP) FindVMIngressIP(ctx context.Context, vmID string) (string, error) {
	return vmIngressIP, o.findIngressIPErr
}

func (o objectFinderForVMIP) FindVMIP(ctx context.Context, vmID string) (string, error) {
	return vmIP, o.findIPErr
}

type mockVMService struct {
	client         client.Client
	stopVMFailed   bool
	startCallCount int
	stopCallCount  int
}

func (v *mockVMService) Start(ctx context.Context, vm *vmv1.VirtualMachine) error {
	vm.Status.State = vmv1.VirtualMachineStateRunning
	if err := v.client.Update(ctx, vm); err != nil {
		return err
	}
	v.startCallCount++
	return nil
}

func (v *mockVMService) Stop(ctx context.Context, vm *vmv1.VirtualMachine) error {
	v.stopCallCount++
	if v.stopVMFailed {
		return errors.New("test failure")
	}
	vm.Status.State = vmv1.VirtualMachineStateStopped
	if err := v.client.Update(ctx, vm); err != nil {
		return err
	}
	return nil
}

func (v *mockVMService) DeleteDisk(ctx context.Context, diskName string) error {
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diskName,
			Namespace: projectID,
		},
	}

	if err := v.client.Delete(ctx, disk); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("delete disk %q: %w", diskName, err)
	}

	return nil
}

type statefulMockAgentService struct {
	storedAgentEnv apiv1.AgentEnv
}

func (s *statefulMockAgentService) Fetch(ctx context.Context, vmName, vmIngressIP string) (apiv1.AgentEnv, error) {
	if s.storedAgentEnv == nil {
		return &apiv1.AgentEnvImpl{}, nil
	}
	return s.storedAgentEnv, nil
}

func (s *statefulMockAgentService) Update(ctx context.Context, vmName string, vmIngressIP string, agentEnv apiv1.AgentEnv) error {
	s.storedAgentEnv = agentEnv
	return nil
}

func (s *statefulMockAgentService) getAgentEnv() (map[string]interface{}, error) {
	agentEnv, err := test.ConvertAgentEnvToJsonMap(s.storedAgentEnv)
	if err != nil {
		return nil, fmt.Errorf("convert agent environment: %w", err)
	}
	return agentEnv, err
}

type statefulMockMetadataClient struct {
	storedAgentEnv apiv1.AgentEnv
}

func (s *statefulMockMetadataClient) Fetch(ctx context.Context, vmName string) (apiv1.AgentEnv, error) {
	if s.storedAgentEnv == nil {
		return &apiv1.AgentEnvImpl{}, nil
	}
	return s.storedAgentEnv, nil
}

func (s *statefulMockMetadataClient) Update(ctx context.Context, vmName string, agentEnv apiv1.AgentEnv) error {
	s.storedAgentEnv = agentEnv
	return nil
}

func (s *statefulMockMetadataClient) getAgentEnv() (map[string]interface{}, error) {
	agentEnv, err := test.ConvertAgentEnvToJsonMap(s.storedAgentEnv)
	if err != nil {
		return nil, fmt.Errorf("convert agent environment: %w", err)
	}
	return agentEnv, err
}

type uuidGeneratorMock struct{}

func (u uuidGeneratorMock) Generate() (uuid string, err error) {
	return mockDiskUUID, nil
}

type mockErrorAgentService struct{}

func (m *mockErrorAgentService) Fetch(ctx context.Context, vmName, vmIngressIP string) (apiv1.AgentEnv, error) {
	return nil, errors.New("agent fetch failure")
}

func (m *mockErrorAgentService) Update(ctx context.Context, vmName string, vmIngressIP string, agentEnv apiv1.AgentEnv) error {
	return errors.New("agent update failure")
}

type mockErrorMetadataClient struct{}

func (m *mockErrorMetadataClient) Fetch(ctx context.Context, vmName string) (apiv1.AgentEnv, error) {
	return nil, errors.New("agent fetch failure")
}

func (m *mockErrorMetadataClient) Update(ctx context.Context, vmName string, agentEnv apiv1.AgentEnv) error {
	return errors.New("agent update failure")
}

type mockUpdateErrorAgentService struct{}

func (m *mockUpdateErrorAgentService) Fetch(ctx context.Context, vmName, vmIngressIP string) (apiv1.AgentEnv, error) {
	return &apiv1.AgentEnvImpl{}, nil
}

func (m *mockUpdateErrorAgentService) Update(ctx context.Context, vmName string, vmIngressIP string, agentEnv apiv1.AgentEnv) error {
	return errors.New("agent update failure")
}

type mockUpdateErrorMetadataClient struct{}

func (m *mockUpdateErrorMetadataClient) Fetch(ctx context.Context, vmName string) (apiv1.AgentEnv, error) {
	return &apiv1.AgentEnvImpl{}, nil
}

func (m *mockUpdateErrorMetadataClient) Update(ctx context.Context, vmName string, agentEnv apiv1.AgentEnv) error {
	return errors.New("agent update failure")
}
func TestAttachDiskV2_HotplugWaitEdgeCases(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")

	tests := []struct {
		name      string
		mutator   func(*vmv1.VirtualMachine)
		expectErr bool
	}{
		{
			name: "status_disks_is_nil",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = nil
			},
			expectErr: true,
		},
		{
			name: "empty_attachments",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{},
				}
			},
			expectErr: true,
		},
		{
			name: "hotpluggable_is_nil",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: diskCID.AsString(), Phase: vmv1.AttachedDiskPhaseReady},
					},
				}
			},
			expectErr: true,
		},
		{
			name: "hotpluggable_is_false",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: diskCID.AsString(), Hotpluggable: ptr.To(false), Phase: vmv1.AttachedDiskPhaseReady},
					},
				}
			},
			expectErr: true,
		},
		{
			name: "name_mismatch",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: "other-disk", Hotpluggable: ptr.To(true), Phase: vmv1.AttachedDiskPhaseReady},
					},
				}
			},
			expectErr: true,
		},
		{
			name: "phase_not_ready",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: diskCID.AsString(), Hotpluggable: ptr.To(true), Phase: vmv1.AttachedDiskPhaseAttaching},
					},
				}
			},
			expectErr: true,
		},
		{
			name: "success",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: diskCID.AsString(), Hotpluggable: ptr.To(true), Phase: vmv1.AttachedDiskPhaseReady},
					},
				}
			},
			expectErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vm := virtualMachine(vmCID.AsString())
			vm.Status.State = vmv1.VirtualMachineStateRunning
			disk := virtualMachineDisk(diskCID.AsString())
			vmAccess := virtualMachineExternalAccess(vmCID.AsString())

			c := test.FakeClient(vm, disk, vmAccess)
			f := service.NewCPIFinder(c, projectID)
			mockAgentService := &statefulMockAgentService{}
			mockMetadataClient := &statefulMockMetadataClient{}
			mockVMSrv := &mockVMService{client: c}
			waiter := mock.NewMockWaiter(c, projectID)

			waiter.TargetObjects["VirtualMachine"] = vm
			waiter.ServerSideActions["VirtualMachine"] = func(ctx context.Context, cl client.Client) error {
				v := &vmv1.VirtualMachine{}
				if err := cl.Get(ctx, client.ObjectKey{Name: vm.Name, Namespace: vm.Namespace}, v); err != nil {
					return err
				}
				tc.mutator(v)
				return cl.Update(ctx, v)
			}

			disks := NewDisks(c, projectID, f, logger, uuidGen, mockAgentService, waiter, mockVMSrv, &test.Timeout, false, true, true, mockMetadataClient)
			_, err := disks.AttachDiskV2(vmCID, diskCID)

			if tc.expectErr {
				if err == nil {
					t.Fatalf("got nil error, want error")
				}
				if !strings.Contains(err.Error(), "wait for disk attachment") {
					t.Fatalf("got error %v, want wait error", err)
				}
			} else {
				if err != nil {
					t.Fatalf("got error %v, want nil", err)
				}
			}
		})
	}
}

func TestDetachDisk_HotplugWaitEdgeCases(t *testing.T) {
	vmCID := apiv1.NewVMCID("vm")
	diskCID := apiv1.NewDiskCID("disk")

	tests := []struct {
		name      string
		mutator   func(*vmv1.VirtualMachine)
		expectErr bool
	}{
		{
			name: "status_disks_is_nil",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = nil
			},
			expectErr: false,
		},
		{
			name: "empty_attachments",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{},
				}
			},
			expectErr: false,
		},
		{
			name: "name_mismatch",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: "other-disk", Hotpluggable: ptr.To(true)},
					},
				}
			},
			expectErr: false,
		},
		{
			name: "still_attached_hotpluggable_true",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: diskCID.AsString(), Hotpluggable: ptr.To(true)},
					},
				}
			},
			expectErr: true,
		},
		{
			name: "still_attached_hotpluggable_false",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: diskCID.AsString(), Hotpluggable: ptr.To(false)},
					},
				}
			},
			expectErr: false,
		},
		{
			name: "still_attached_hotpluggable_nil",
			mutator: func(v *vmv1.VirtualMachine) {
				v.Status.Disks = &vmv1.VirtualMachineAttachedDisksInfo{
					Attachments: []vmv1.VirtualMachineAttachedDiskStatus{
						{Name: diskCID.AsString()},
					},
				}
			},
			expectErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vm := virtualMachine(vmCID.AsString())
			vm.Status.State = vmv1.VirtualMachineStateRunning

			vm.Spec.Disks = []vmv1.DiskAttachment{
				{VirtualMachineDiskRef: v1.LocalObjectReference{Name: diskCID.AsString()}},
			}

			disk := virtualMachineDisk(diskCID.AsString())
			vmAccess := virtualMachineExternalAccess(vmCID.AsString())

			c := test.FakeClient(vm, disk, vmAccess)
			f := service.NewCPIFinder(c, projectID)
			mockAgentService := &statefulMockAgentService{}
			mockMetadataClient := &statefulMockMetadataClient{}
			mockVMSrv := &mockVMService{client: c}
			waiter := mock.NewMockWaiter(c, projectID)

			waiter.TargetObjects["VirtualMachine"] = vm
			waiter.ServerSideActions["VirtualMachine"] = func(ctx context.Context, cl client.Client) error {
				v := &vmv1.VirtualMachine{}
				if err := cl.Get(ctx, client.ObjectKey{Name: vm.Name, Namespace: vm.Namespace}, v); err != nil {
					return err
				}
				tc.mutator(v)
				return cl.Update(ctx, v)
			}

			disks := NewDisks(c, projectID, f, logger, uuidGen, mockAgentService, waiter, mockVMSrv, &test.Timeout, false, true, true, mockMetadataClient)
			err := disks.DetachDisk(vmCID, diskCID)

			if tc.expectErr {
				if err == nil {
					t.Fatalf("got nil error, want error")
				}
				if !strings.Contains(err.Error(), "wait for disk detachment") {
					t.Fatalf("got error %v, want wait error", err)
				}
			} else {
				if err != nil {
					t.Fatalf("got error %v, want nil", err)
				}
			}
		})
	}
}

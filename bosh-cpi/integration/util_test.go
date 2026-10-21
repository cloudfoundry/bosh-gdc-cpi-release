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

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/hashicorp/go-multierror"
	"golang.org/x/exp/slices"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	k8snetworkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/disk"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/stemcell"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/unit"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/vm"
	networkingv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/networking/v1"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

const (
	createVMMethod        = "create_vm"
	hasVMMethod           = "has_vm"
	setVMMetadataMethod   = "set_vm_metadata"
	rebootVMMethod        = "reboot_vm"
	deleteVMMethod        = "delete_vm"
	createDiskMethod      = "create_disk"
	hasDiskMethod         = "has_disk"
	deleteDiskMethod      = "delete_disk"
	resizeDiskMethod      = "resize_disk"
	setDiskMetadataMethod = "set_disk_metadata"
	getDisksMethod        = "get_disks"
	snapshotDiskMethod    = "snapshot_disk"
	deleteSnapshotMethod  = "delete_snapshot"
	attachDiskMethod      = "attach_disk"
	detachDiskMethod      = "detach_disk"
	createStemcellMethod  = "create_stemcell"
	deleteStemcellMethod  = "delete_stemcell"
	zoneKey               = "zone"

	// the mininum supported disk space on lancer in 20MiB, VMM team mentioned
	// in future they might support the disk space less than 20MiB too.
	diskSizeMiB = 30
)

var (
	ctx                      = context.Background()
	defaultDiskGB            = 20
	defaultEphemeralDiskGB   = 10
	defaultDiskType          = consts.DiskTypeStandard
	projectNetworkPolicyName = "bosh-allow-external-traffic-vm"
)

func testCreateVM(imageName string, t *testing.T, mzConfig ...mzTestConfig) string {
	kubeClient := getKubeClient(mzConfig...)
	vt, err := getVMTypeWithLeastCPU(kubeClient, gdcConfig.UseTier1Network)
	if err != nil {
		t.Fatalf("vm type: %v", err)
	}

	lb := vm.LB{
		Name: "test-lb",
		Selector: vm.LBSelector{
			Key:   "test-key",
			Value: "test-value",
		},
	}
	vmProp, err := vmCloudProperty(vt.Name, int(vt.Spec.VCPUs), lb, mzConfig...)
	if err != nil {
		t.Errorf("vm prop: %v", err)
	}

	args := []interface{}{
		apiv1.NewAgentID("cpi-test"),
		apiv1.NewStemcellCID(imageName),
		vmProp,
		apiv1.Networks{},
		[]apiv1.DiskCID{},
		apiv1.VMEnv{},
	}

	request := getRequest(createVMMethod, args)
	gotJson, err := executor.exec(request)
	if err != nil {
		t.Fatalf("create vm: %v", err)
	}

	var gotArr []interface{}
	if err := json.Unmarshal(gotJson, &gotArr); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	vmID, ok := gotArr[0].(string)
	if !ok {
		t.Fatal("parse vmCID failed")
	}

	vm, err := getVM(kubeClient, vmID)
	if err != nil {
		t.Fatalf("find vm (%s): %v", vmID, err)
	}

	bootDiskName := fmt.Sprintf("%s-boot-disk", vm.Name)
	ephemeralDiskName := fmt.Sprintf("%s-ephemeral-disk", vm.Name)

	opts := []cmp.Option{
		cmpopts.IgnoreFields(vmv1.NetworkInterfaceSpec{}, "Subnet", "SubnetNamespace"),
	}
	expectedVM := expectedVM(vt.Name, vm.Name, bootDiskName, ephemeralDiskName, gdcConfig.UseTier1Network)
	if diff := cmp.Diff(expectedVM.Spec, vm.Spec, opts...); diff != "" {
		t.Fatalf("vm diff (-want +got):\n%s", diff)
	}
	if vm.Labels[lb.Selector.Key] != lb.Selector.Value {
		t.Fatalf("VM %s does not have label with key %q and value %v associated with the assigned LoadBalancer %q\n", vm.Name, lb.Selector.Key, lb.Selector.Key, lb.Name)
	}
	if vm.Labels[consts.VMExternalEgressControlLabelKey] != "true" {
		t.Fatalf("VM %q does not have label with key %q and value %v\n", vm.Name, consts.VMExternalEgressControlLabelKey, lb.Name)
	}
	if len(mzConfig) > 0 && vm.Labels[zoneKey] != mzConfig[0].zone {
		t.Fatalf("VM %q created from multi-zone CPI does not have label with key %q and value %v\n", vm.Name, zoneKey, mzConfig[0].zone)
	}
	if gdcConfig.UseDiskHotplug {
		if val, ok := vm.Annotations[consts.DiskHotPlugAnnotationKey]; !ok || val != "true" {
			t.Fatalf("VM %q should have annotation %q with value 'true', but got value %q (present: %t)", vm.Name, consts.DiskHotPlugAnnotationKey, val, ok)
		}
	} else {
		if _, ok := vm.Annotations[consts.DiskHotPlugAnnotationKey]; ok {
			t.Fatalf("VM %q has annotation with key %q, but it should not be present", vm.Name, consts.DiskHotPlugAnnotationKey)
		}
	}

	// Verify boot disk
	bootDisk := &vmv1.VirtualMachineDisk{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: bootDiskName, Namespace: projectID}, bootDisk); err != nil {
		t.Fatalf("find boot disk (%s): %v", bootDiskName, err)
	}

	bootDiskResource := resource.NewQuantity(int64(defaultDiskGB*unit.GiB), resource.BinarySI)
	if bootDisk.Spec.Size.Cmp(*bootDiskResource) != 0 {
		t.Fatalf("test failed, got boot disk size (%v), want (%v)", bootDisk.Spec.Size, bootDiskResource)
	}

	wantDiskType := vmv1.DiskTypeStandard
	gotBootDiskType := bootDisk.Spec.Type
	if wantDiskType != gotBootDiskType {
		t.Fatalf("CreateVM(): got boot disk type: %v, want boot disk type: %v", gotBootDiskType, wantDiskType)
	}
	ephemeralDisk := &vmv1.VirtualMachineDisk{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: ephemeralDiskName, Namespace: projectID}, ephemeralDisk); err != nil {
		t.Fatalf("kubeClient.Get(%s): %v", ephemeralDiskName, err)
	}
	gotEphemeralDiskType := ephemeralDisk.Spec.Type
	if wantDiskType != gotEphemeralDiskType {
		t.Fatalf("CreateVM(): got ephemeral disk type: %v, want ephemeral disk type: %v", gotEphemeralDiskType, wantDiskType)
	}

	return vm.Name
}

func testHasVM(vmID string, t *testing.T, mzConfig ...mzTestConfig) {
	args := []interface{}{apiv1.NewVMCID(vmID)}
	request := getRequest(hasVMMethod, args)
	gotJson, err := executor.exec(request)
	if err != nil {
		t.Fatal(err)
	}

	var hasVM bool
	if err := json.Unmarshal(gotJson, &hasVM); err != nil {
		t.Fatalf("unmarshall result: %v", err)
	}
	if !hasVM {
		t.Fatalf("VM with id %s not found", vmID)
	}
}

func testSetVMMetadata(vmID string, t *testing.T, mzConfig ...mzTestConfig) {
	metadata := map[string]interface{}{
		"created_at": "2024-01-04T16:03:38Z",
		"name":       "bosh/0",
	}
	args := []interface{}{apiv1.NewVMCID(vmID), apiv1.NewVMMeta(metadata)}
	request := getRequest(setVMMetadataMethod, args)
	_, err := executor.exec(request)
	if err != nil {
		t.Fatal(err)
	}

	kubeClient := getKubeClient(mzConfig...)
	vm, err := getVM(kubeClient, vmID)
	if err != nil {
		t.Fatalf("VM with id %s not found", vmID)
	}

	expected := map[string]string{
		"created_at": "20240104T160338Z",
		"name":       "bosh--0",
	}
	if err := verifySubMap(expected, vm.ObjectMeta.Labels); err != nil {
		t.Fatal(err)
	}
}

func testRebootVM(vmID string, t *testing.T, mzConfig ...mzTestConfig) {
	args := []interface{}{apiv1.NewVMCID(vmID)}
	request := getRequest(rebootVMMethod, args)
	if _, err := executor.exec(request); err != nil {
		t.Fatal(err)
	}

	kubeClient := getKubeClient(mzConfig...)
	vm, err := getVM(kubeClient, vmID)
	if err != nil {
		t.Fatalf("reboot: %v", err)
	}

	if vm.Status.State != vmv1.VirtualMachineStateRunning {
		t.Fatalf("got vm state %s, expected %s", vm.Status.State, vmv1.VirtualMachineStateRunning)
	}
}

func testDeleteVM(vmID string, t *testing.T, mzConfig ...mzTestConfig) {
	args := []interface{}{apiv1.NewVMCID(vmID)}
	request := getRequest(deleteVMMethod, args)
	if _, err := executor.exec(request); err != nil {
		t.Fatal(err)
	}

	kubeClient := getKubeClient(mzConfig...)
	_, err := getVM(kubeClient, vmID)
	if err == nil {
		t.Fatalf("got vm %s, expected not found", vmID)
	}

	if !apierrors.IsNotFound(err) {
		t.Fatalf("failed to get vm %s: %v", vmID, err)
	}
}

func testCreateDisk(t *testing.T, mzConfig ...mzTestConfig) string {
	prop := disk.Property{}
	vmId := "test"
	if len(mzConfig) > 0 {
		prop.Zone = mzConfig[0].zone
		vmId = mzConfig[0].vmId
	}
	prop.DiskType = consts.DiskTypeStandard
	jsonData, err := json.Marshal(prop)
	if err != nil {
		t.Errorf("disk prop: %v", err)
	}
	args := []interface{}{
		diskSizeMiB,
		apiv1.CloudPropsImpl{
			RawMessage: json.RawMessage(jsonData),
		},
		apiv1.NewVMCID(vmId),
	}
	request := getRequest(createDiskMethod, args)
	gotJson, err := executor.exec(request)
	if err != nil {
		t.Fatalf("create disk: %v", err)
	}

	var diskID string
	if err := json.Unmarshal(gotJson, &diskID); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	// Verify Disk
	kubeClient := getKubeClient(mzConfig...)
	disk, err := getDisk(kubeClient, diskID)
	if err != nil {
		t.Fatalf("get disk: %v", err)
	}

	expectedSize := resource.MustParse(fmt.Sprintf("%vMi", diskSizeMiB))
	if expectedSize.Cmp(disk.Status.Size) != 0 {
		t.Fatalf("actual size: %v, expected: %v", disk.Spec.Size, expectedSize)
	}
	wantDiskType := vmv1.DiskTypeStandard
	gotDiskType := disk.Spec.Type
	if wantDiskType != gotDiskType {
		t.Fatalf("CreateDisk(): got disk type: %v, want disk type: %v", gotDiskType, wantDiskType)
	}

	return disk.Name
}

func testHasDisk(diskID string, t *testing.T, mzConfig ...mzTestConfig) {
	args := []interface{}{apiv1.NewDiskCID(diskID)}
	request := getRequest(hasDiskMethod, args)
	gotJson, err := executor.exec(request)
	if err != nil {
		t.Fatal(err)
	}

	var hasDisk bool
	if err := json.Unmarshal(gotJson, &hasDisk); err != nil {
		t.Fatalf("unmarshall result: %v", err)
	}
	if !hasDisk {
		t.Fatalf("Disk with id %s not found", diskID)
	}
}

func testSetDiskMetadata(diskID string, t *testing.T, mzConfig ...mzTestConfig) {
	metadata := map[string]interface{}{
		"created_at": "2024-02-05T16:03:38Z",
		"job":        "test",
	}
	args := []interface{}{apiv1.NewDiskCID(diskID), apiv1.NewDiskMeta(metadata)}
	request := getRequest(setDiskMetadataMethod, args)

	_, err := executor.exec(request)
	if err != nil {
		t.Fatal(err)
	}

	kubeClient := getKubeClient(mzConfig...)
	disk, err := getDisk(kubeClient, diskID)
	if err != nil {
		t.Fatalf("Disk with id %s not found", diskID)
	}

	expected := map[string]string{
		"created_at": "20240205T160338Z",
		"job":        "test",
	}
	if err := verifySubMap(expected, disk.ObjectMeta.Labels); err != nil {
		t.Fatal(err)
	}
}

func testResizeDisk(diskID string, t *testing.T, mzConfig ...mzTestConfig) {
	args := []interface{}{apiv1.NewDiskCID(diskID), 2000}
	request := getRequest(resizeDiskMethod, args)
	_, err := executor.exec(request)

	if err != nil {
		t.Fatalf("got error: %v, expected nil", err)
	}

	kubeClient := getKubeClient(mzConfig...)
	disk, err := getDisk(kubeClient, diskID)
	if err != nil {
		t.Fatalf("Disk with id %q not found", diskID)
	}
	expectedSize := resource.MustParse(fmt.Sprintf("%vMi", 2000))
	if disk.Status.Size.Cmp(expectedSize) != 0 {
		t.Fatalf("disk size %v is not equal to expected size %v", disk.Status.Size, expectedSize)
	}
}

func testResizeDiskHotExpansion(t *testing.T, mzConfig ...mzTestConfig) {
	kubeClient := getKubeClient(mzConfig...)
	imageName := getImageName(kubeClient, t)
	vmID := ""
	if len(mzConfig) > 0 {
		vmID = mzConfig[0].vmId
	}

	if vmID == "" {
		vt, err := getVMTypeWithLeastCPU(kubeClient, false)
		if err != nil {
			t.Fatalf("vm type: %v", err)
		}
		lb := vm.LB{}
		vmProp, err := vmCloudProperty(vt.Name, int(vt.Spec.VCPUs), lb, mzConfig...)
		if err != nil {
			t.Errorf("vm prop: %v", err)
		}
		args := []interface{}{
			apiv1.NewAgentID("cpi-test"),
			apiv1.NewStemcellCID(imageName),
			vmProp,
			apiv1.Networks{},
			[]apiv1.DiskCID{},
			apiv1.VMEnv{},
		}

		request := getRequest(createVMMethod, args)
		gotJson, err := executor.exec(request)
		if err != nil {
			t.Fatalf("create vm: %v", err)
		}

		var gotArr []interface{}
		if err := json.Unmarshal(gotJson, &gotArr); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		ok := false
		vmID, ok = gotArr[0].(string)
		if !ok {
			t.Fatal("parse vmCID failed")
		}
	}

	vm, err := getVM(kubeClient, vmID)
	if err != nil {
		t.Fatalf("find vm (%s): %v", vmID, err)
	}

	bootDiskName := fmt.Sprintf("%s-boot-disk", vm.Name)
	bootDisk := &vmv1.VirtualMachineDisk{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: bootDiskName, Namespace: projectID}, bootDisk); err != nil {
		t.Fatalf("find boot disk (%s): %v", bootDiskName, err)
	}

	bootDiskResource := resource.NewQuantity(int64(defaultDiskGB*unit.GiB), resource.BinarySI)
	if bootDisk.Spec.Size.Cmp(*bootDiskResource) != 0 {
		t.Fatalf("test failed, got boot disk size (%v), want (%v)", bootDisk.Spec.Size, bootDiskResource)
	}
	diskID := bootDisk.Name
	increment := 2
	newDiskSize := defaultDiskGB + increment
	args := []interface{}{apiv1.NewDiskCID(diskID), newDiskSize * 1024}
	request := getRequest(resizeDiskMethod, args)
	_, err = executor.exec(request)
	if err != nil {
		t.Fatalf("got error: %v, expected nil", err)
	}

	disk, err := getDisk(kubeClient, diskID)
	if err != nil {
		t.Fatalf("Disk with id %q not found", diskID)
	}
	expectedSize := resource.MustParse(fmt.Sprintf("%vGi", newDiskSize))
	if disk.Status.Size.Cmp(expectedSize) != 0 {
		t.Fatalf("disk size %v is not equal to expected size %v", disk.Status.Size, expectedSize)
	}

	// If this condition is met then the vm is created during this particular test, so deleting the vm.
	if len(mzConfig) == 0 || mzConfig[0].vmId == "" {
		testDeleteVM(vmID, t, mzConfig...)
	}
}

func testSnapshotDisk(diskID string, t *testing.T, mzConfig ...mzTestConfig) string {
	args := []interface{}{
		apiv1.NewDiskCID(diskID),
		apiv1.DiskMeta{},
	}

	request := getRequest(snapshotDiskMethod, args)
	gotJson, err := executor.exec(request)
	if err != nil {
		t.Fatalf("snapshot disk: %v", err)
	}

	var backupID string
	if err := json.Unmarshal(gotJson, &backupID); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// Verify backup
	kubeClient := getKubeClient(mzConfig...)
	exists, err := hasBackup(kubeClient, backupID)
	if err != nil {
		t.Fatalf("get backup: %v", err)
	}
	if !exists {
		t.Fatalf("backup %s not found", backupID)
	}

	return backupID
}

func testDeleteSnapshot(backupID string, t *testing.T, mzConfig ...mzTestConfig) {
	args := []interface{}{
		apiv1.NewSnapshotCID(backupID),
	}

	request := getRequest(deleteSnapshotMethod, args)
	if _, err := executor.exec(request); err != nil {
		t.Fatalf("delete snapshot: %v", err)
	}

	// Verify backup
	kubeClient := getKubeClient(mzConfig...)
	exists, err := hasBackup(kubeClient, backupID)
	if err != nil {
		t.Fatalf("get backup: %v", err)
	}

	if exists {
		t.Fatalf("unexpected backup %q", backupID)
	}
}

func testDeleteDisk(diskID string, t *testing.T, mzConfig ...mzTestConfig) {
	args := []interface{}{apiv1.NewDiskCID(diskID)}
	request := getRequest(deleteDiskMethod, args)
	if _, err := executor.exec(request); err != nil {
		t.Fatal(err)
	}

	kubeClient := getKubeClient(mzConfig...)
	_, err := getDisk(kubeClient, diskID)
	if err == nil {
		t.Fatalf("got disk %s, expected not found", diskID)
	}

	if !apierrors.IsNotFound(err) {
		t.Fatalf("failed to get disk %s: %v", diskID, err)
	}
}

func testGetDisks(vmID string, t *testing.T, mzConfig ...mzTestConfig) {
	args := []interface{}{
		apiv1.NewVMCID(vmID),
	}

	request := getRequest(getDisksMethod, args)
	gotJson, err := executor.exec(request)
	if err != nil {
		t.Fatalf("get disks: %v", err)
	}

	var diskIDs []string
	if err := json.Unmarshal(gotJson, &diskIDs); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}

	// Verify Disks
	kubeClient := getKubeClient(mzConfig...)
	expectedVMDisks, err := getVMDisks(kubeClient, vmID)
	if err != nil {
		t.Fatal(err)
	}

	opts := cmp.Options{
		cmpopts.SortSlices(func(a, b string) bool { return a < b }),
	}
	if diff := cmp.Diff(diskIDs, expectedVMDisks, opts); diff != "" {
		t.Errorf("test disks diff (-want +got):\n%s", diff)
	}
}

func testDiskWithVMMethods(vmID string, t *testing.T, mzTestConfig ...mzTestConfig) {
	// Prepare
	diskID := testCreateDisk(t, mzTestConfig...)

	t.Run("AttachDisk", func(t *testing.T) {
		testAttachDisk(vmID, diskID, t, mzTestConfig...)
	})

	t.Run("DetachDisk", func(t *testing.T) {
		testDetachDisk(vmID, diskID, t, mzTestConfig...)
	})

	// Cleanup
	testDeleteDisk(diskID, t, mzTestConfig...)
}

func testAttachDisk(vmID, diskID string, t *testing.T, mzTestConfig ...mzTestConfig) {
	args := []interface{}{apiv1.NewVMCID(vmID), apiv1.NewDiskCID(diskID)}
	request := getRequest(attachDiskMethod, args)
	if _, err := executor.exec(request); err != nil {
		t.Fatal(err)
	}

	gotJson, err := executor.exec(request)
	if err != nil {
		t.Fatal(err)
	}

	var gotDiskPath string
	if err := json.Unmarshal(gotJson, &gotDiskPath); err != nil {
		t.Fatalf("unmarshall result: %v", err)
	}

	diskPath := fmt.Sprintf("/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_%s", diskID)
	if gotDiskPath != diskPath {
		t.Fatalf("got disk path %s, expected %s", gotDiskPath, diskPath)
	}

	kubeClient := getKubeClient(mzTestConfig...)
	vmDisks, err := getVMDisks(kubeClient, vmID)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(vmDisks, diskID) {
		t.Fatalf("vm %s does not contain attached disk %s", vmID, diskID)
	}
}

func testDetachDisk(vmID, diskID string, t *testing.T, mzTestConfig ...mzTestConfig) {
	args := []interface{}{apiv1.NewVMCID(vmID), apiv1.NewDiskCID(diskID)}
	request := getRequest(detachDiskMethod, args)
	if _, err := executor.exec(request); err != nil {
		t.Fatal(err)
	}

	_, err := executor.exec(request)
	if err != nil {
		t.Fatal(err)
	}

	kubeClient := getKubeClient(mzTestConfig...)
	vmDisks, err := getVMDisks(kubeClient, vmID)
	if err != nil {
		t.Fatal(err)
	}

	if slices.Contains(vmDisks, diskID) {
		t.Fatalf("vm %s contains detached disk %s", vmID, diskID)
	}
}

func testCreateStemcell(kubeClient client.WithWatch, stemcellImagePath string, stemcellManifestPath string, t *testing.T) string {
	yamlFile, err := os.ReadFile(stemcellManifestPath)
	if err != nil {
		t.Fatalf("read YAML file: %v", err)
	}

	stemcellCloudProps, err := getCloudProperties(yamlFile)
	if err != nil {
		t.Fatalf("get cloud properties: %v", err)
	}

	var props stemcell.Property
	if err := stemcellCloudProps.As(&props); err != nil {
		t.Fatalf("parse cloud properties: %v", err)
	}

	args := []interface{}{
		stemcellImagePath,
		props,
	}

	request := getRequest(createStemcellMethod, args)
	gotJson, err := executor.exec(request)
	if err != nil {
		t.Fatalf("create stemcell: %v", err)
	}

	var stemcellCID string
	if err := json.Unmarshal(gotJson, &stemcellCID); err != nil {
		t.Fatalf("unmarshal stemcell CID: %v", err)
	}

	ctx := context.Background()
	vmi := &vmv1.VirtualMachineImage{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: stemcellCID, Namespace: projectID}, vmi); err != nil {
		t.Fatalf("find stemcell (%s): %v", stemcellCID, err)
	}

	return stemcellCID
}

func testDeleteStemcell(kubeClient client.Client, stemcellCID string, t *testing.T) {
	args := []interface{}{
		apiv1.NewStemcellCID(stemcellCID),
	}
	request := getRequest(deleteStemcellMethod, args)

	_, err := executor.exec(request)
	if err != nil {
		t.Fatalf("delete stemcell (%s): %v", stemcellCID, err)
	}

	importObj := &vmv1.VirtualMachineImageImport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stemcellCID,
			Namespace: projectID,
		},
	}
	if err := kubeClient.Delete(ctx, importObj); err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("delete VirtualMachineImageImport (%s): %v", stemcellCID, err)
	}
}

func getVM(kubeClient client.Client, vmID string) (*vmv1.VirtualMachine, error) {
	vm := &vmv1.VirtualMachine{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: vmID, Namespace: projectID}, vm); err != nil {
		return nil, err
	}
	return vm, nil
}

func getDisk(kubeClient client.WithWatch, diskID string) (*vmv1.VirtualMachineDisk, error) {
	disk := &vmv1.VirtualMachineDisk{}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Name: diskID, Namespace: projectID}, disk); err != nil {
		return nil, err
	}
	return disk, nil
}

func getVMDisks(kubeClient client.WithWatch, vmID string) ([]string, error) {
	ctx := context.Background()
	vm := &vmv1.VirtualMachine{}
	if err := kubeClient.Get(ctx, client.ObjectKey{Name: vmID, Namespace: projectID}, vm); err != nil {
		return nil, fmt.Errorf("failed to get vm %s: %w", vmID, err)
	}

	var diskNames []string
	for _, disk := range vm.Spec.Disks {
		diskNames = append(diskNames, disk.VirtualMachineDiskRef.Name)
	}

	return diskNames, nil
}

func hasBackup(c client.Client, backupName string) (bool, error) {
	ctx := context.Background()
	backup := &vmv1.VirtualMachineBackup{}

	if err := c.Get(ctx, client.ObjectKey{Name: backupName, Namespace: projectID}, backup); apierrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to get backup %s: %w", backupName, err)
	}

	return true, nil
}

func getImageName(kubeClient client.WithWatch, t *testing.T) string {
	var imageName string
	var err error

	if useStemcell {
		logger.Info(logTag, "Using stemcell image for CreateVM integration test")
		imageName = testCreateStemcell(kubeClient, gdcConfig.StemcellInfo.ImagePath, gdcConfig.StemcellInfo.ManifestPath, t)
	} else {
		logger.Info(logTag, "Using golden vm image for CreateVM integration test")
		imageName, err = getGoldenImageName(kubeClient)
		if err != nil {
			t.Fatalf("Error getting golden image name: %v", err)
		}
	}

	return imageName
}

func getGoldenImageName(kubeClient client.WithWatch) (string, error) {
	var vmimages vmv1.VirtualMachineImageList
	if err := kubeClient.List(ctx, &vmimages, &client.ListOptions{Namespace: consts.VMSystemNamespace}); err != nil {
		return "", err
	}

	for _, vmi := range vmimages.Items {
		if vmi.Spec.OperatingSystem.Name == consts.Ubuntu2404 {
			return vmi.Name, nil
		}
	}
	return "", fmt.Errorf("cannot find golden image with OS %s", consts.Ubuntu2404)
}

func getVMTypeWithLeastCPU(kubeClient client.WithWatch, useTier1Network bool) (*vmv1.VirtualMachineType, error) {
	vmTypeList := vmv1.VirtualMachineTypeList{}
	if err := kubeClient.List(context.Background(), &vmTypeList, client.InNamespace(consts.VMSystemNamespace)); err != nil {
		return nil, err
	}

	if useTier1Network {
		var bestCandidate *vmv1.VirtualMachineType
		for i, vt := range vmTypeList.Items {
			if vt.Status.Supported != nil && *vt.Status.Supported && vm.IsTier1NetworkSupported(vt.Name, vt.Spec.VCPUs) {
				if bestCandidate == nil || vt.Spec.VCPUs < bestCandidate.Spec.VCPUs {
					bestCandidate = &vmTypeList.Items[i]
				}
			}
		}

		if bestCandidate != nil {
			logger.Info(logTag, "Choose Tier 1-supported vm type with least number of vCPUs: %s with %d vCPUs", bestCandidate.Name, bestCandidate.Spec.VCPUs)
			return bestCandidate, nil
		}
		return nil, fmt.Errorf("no Tier 1 network supported vm type found")
	}

	var vmTypeWithLeastCPU *vmv1.VirtualMachineType
	for i, vt := range vmTypeList.Items {
		if vt.Status.Supported != nil && *vt.Status.Supported && (vmTypeWithLeastCPU == nil || vt.Spec.VCPUs < vmTypeWithLeastCPU.Spec.VCPUs) {
			vmTypeWithLeastCPU = &vmTypeList.Items[i]
		}
	}

	if vmTypeWithLeastCPU != nil {
		logger.Info(logTag, "Choose vm type with least CPUs: %s with %d vCPUs", vmTypeWithLeastCPU.Name, vmTypeWithLeastCPU.Spec.VCPUs)
		return vmTypeWithLeastCPU, nil
	}

	return nil, fmt.Errorf("no supported vm type found")
}

func vmCloudProperty(vmTypeName string, vmVPCUs int, lb vm.LB, mzConfig ...mzTestConfig) (apiv1.VMCloudProps, error) {
	prop := vm.Property{
		VirtualMachineType:  vmTypeName,
		VCPUs:               vmVPCUs,
		BootDiskSizeGB:      defaultDiskGB,
		EphemeralDiskSizeGB: defaultEphemeralDiskGB,
		BootDiskType:        defaultDiskType,
		EphemeralDiskType:   defaultDiskType,
		LoadBalancer:        lb,
	}
	targetedZone := ""
	if len(mzConfig) > 0 {
		targetedZone = mzConfig[0].zone
	}
	if len(targetedZone) > 0 {
		prop.Zone = targetedZone
	}
	jsonData, err := json.Marshal(prop)
	if err != nil {
		return nil, err
	}

	return apiv1.CloudPropsImpl{
		RawMessage: json.RawMessage(jsonData),
	}, nil
}

func expectedVM(vmTypeName, vmName, bootDiskName string, ephemeralDiskName string, useTier1Network bool) *vmv1.VirtualMachine {
	vm := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: projectID,
		},
		Spec: vmv1.VirtualMachineSpec{
			Compute: vmv1.Compute{
				VirtualMachineType: vmTypeName,
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
			Network: &vmv1.NetworkSpec{
				Interfaces: []vmv1.NetworkInterfaceSpec{
					{
						Network: vmv1.NetworkDefault,
					},
				},
			},
		},
	}

	if useTier1Network {
		vm.Spec.Network.NetworkPerformanceTier = consts.NetworkPerformanceTier1
	} else {
		vm.Spec.Network.NetworkPerformanceTier = consts.NetworkPerformanceTierDefault
	}

	return vm
}

func getCloudProperties(yamlFile []byte) (apiv1.CloudPropsImpl, error) {
	var yamlData map[string]interface{}
	err := yaml.Unmarshal(yamlFile, &yamlData)
	if err != nil {
		return apiv1.CloudPropsImpl{}, fmt.Errorf("parse YAML: %w", err)
	}

	cloudProps, ok := yamlData["cloud_properties"]
	if !ok {
		return apiv1.CloudPropsImpl{}, fmt.Errorf("cloud_properties key not found in YAML")
	}

	cloudPropsJSON, err := json.Marshal(cloudProps)
	if err != nil {
		return apiv1.CloudPropsImpl{}, fmt.Errorf("marshal cloud properties: %w", err)
	}

	return apiv1.CloudPropsImpl{
		RawMessage: json.RawMessage(cloudPropsJSON),
	}, nil
}

func verifySubMap(subMap map[string]string, mainMap map[string]string) error {
	var errs *multierror.Error

	// Check if all target keys are present in the sub-map
	for k, v := range subMap {
		got, ok := mainMap[k]
		if !ok {
			errs = multierror.Append(errs, fmt.Errorf("key %s is missing", k))
		}

		if v != got {
			errs = multierror.Append(errs, fmt.Errorf("value for key %s does not match, got (%s), expected (%s)", k, got, v))
		}
	}
	return errs.ErrorOrNil()
}

func createProjectNetworkPolicy(kubeClient client.WithWatch) error {
	pnp := &networkingv1.ProjectNetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projectNetworkPolicyName,
			Namespace: projectID,
		},
		Spec: networkingv1.ProjectNetworkPolicySpec{
			Ingress: []networkingv1.ProjectNetworkPolicyIngressRule{
				{
					From: []networkingv1.ProjectNetworkPolicyPeer{
						{
							IPBlock: &k8snetworkingv1.IPBlock{CIDR: "0.0.0.0/0"},
						},
					},
				},
			},
			PolicyType: networkingv1.PolicyTypeIngress,
			Subject: networkingv1.ProjectNetworkPolicySubject{
				SubjectType: networkingv1.PolicySubjectTypeUserWorkload,
			},
		},
	}

	foundPnp := &networkingv1.ProjectNetworkPolicy{}
	switch err := kubeClient.Get(ctx, client.ObjectKeyFromObject(pnp), foundPnp); {
	case err == nil:
		// PNP already exists
		return nil
	case apierrors.IsNotFound(err):
		// Create PNP
		if err := kubeClient.Create(ctx, pnp); err != nil {
			return fmt.Errorf("failed to create ProjectNetworkPolicy %s to access VM: %w", pnp.Name, err)
		}
	default:
		// Failed to get PNP
		return fmt.Errorf("failed to get ProjectNetworkPolicy %s to access VM: %w", pnp.Name, err)
	}
	return nil
}

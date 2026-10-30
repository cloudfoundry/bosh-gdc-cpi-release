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
	"fmt"
	"reflect"
	"strings"
	"time"

	"k8s.io/utils/clock"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/utils/ptr"

	backupv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/backup/v1"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/bosherr"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/unit"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/util"
)

const (
	// VM backup plan with name `disk-snapshot-bpt` is required
	// https://cloud.google.com/distributed-cloud/hosted/docs/1.7/user/application/ao-user/vms/backup-and-restore/back-up-disks
	diskSnapshotBackupPlanTemplate = "disk-snapshot-bpt"
)

const (
	// Disk operation types
	diskOperationAttach = "attach"
	diskOperationDetach = "detach"
)

type objectFinder interface {
	HasDisk(ctx context.Context, diskID string) (bool, error)
	FindDisk(ctx context.Context, diskID string) (*vmv1.VirtualMachineDisk, error)
	FindVM(ctx context.Context, vmID string) (*vmv1.VirtualMachine, error)
	FindVMIP(ctx context.Context, vmID string) (string, error)
	FindVMIngressIP(ctx context.Context, vmID string) (string, error)
}

type uuidGenerator interface {
	Generate() (uuid string, err error)
}

type agentClient interface {
	Fetch(ctx context.Context, vmName, vmIngressIP string) (apiv1.AgentEnv, error)
	Update(ctx context.Context, vmName string, vmIngressIP string, agentEnv apiv1.AgentEnv) error
}

type metadataClient interface {
	Fetch(ctx context.Context, vmName string) (apiv1.AgentEnv, error)
	Update(ctx context.Context, vmName string, agentEnv apiv1.AgentEnv) error
}

type vmService interface {
	Start(ctx context.Context, vm *vmv1.VirtualMachine) error
	Stop(ctx context.Context, vm *vmv1.VirtualMachine) error
	DeleteDisk(ctx context.Context, diskName string) error
}

type waiter interface {
	Until(ctx context.Context, check wait.ConditionChecker, name string, obj client.Object, timeout time.Duration) error
	UntilDeleted(ctx context.Context, name string, obj client.Object, timeout time.Duration) error
}

type Disks struct {
	cloudClient            client.Client
	projectID              string
	finder                 objectFinder
	logger                 cfboshlog.Logger
	uuidGen                uuidGenerator
	vmService              vmService
	agentEnvService        agentClient
	waiter                 waiter
	clock                  clock.PassiveClock
	timeout                *config.WaitTimeout
	enableHotDiskExpansion bool
	useMetadataServer      bool
	useDiskHotplug         bool
	metadataClient         metadataClient
}

func NewDisks(
	cloudClient client.Client,
	projectID string,
	finder objectFinder,
	logger cfboshlog.Logger,
	uuidGen uuidGenerator,
	agentEnvService agentClient,
	waiter waiter,
	vmService vmService,
	timeout *config.WaitTimeout,
	enableHotDiskExpansion bool,
	useMetadataServer bool,
	useDiskHotplug bool,
	metadataClient metadataClient,
) *Disks {
	return &Disks{
		cloudClient:            cloudClient,
		projectID:              projectID,
		finder:                 finder,
		logger:                 logger,
		uuidGen:                uuidGen,
		vmService:              vmService,
		agentEnvService:        agentEnvService,
		waiter:                 waiter,
		clock:                  clock.RealClock{},
		timeout:                timeout,
		enableHotDiskExpansion: enableHotDiskExpansion,
		useMetadataServer:      useMetadataServer,
		useDiskHotplug:         useDiskHotplug,
		metadataClient:         metadataClient,
	}
}

// Disk APIs.
func (d *Disks) GetDisks(cid apiv1.VMCID) ([]apiv1.DiskCID, error) {
	ctx := context.Background()

	vm, err := d.finder.FindVM(ctx, cid.AsString())
	if err != nil {
		return nil, fmt.Errorf("find vm: %w", err)
	}

	diskCIDs := make([]apiv1.DiskCID, len(vm.Spec.Disks))
	for i, disk := range vm.Spec.Disks {
		diskCIDs[i] = apiv1.NewDiskCID(disk.VirtualMachineDiskRef.Name)
	}

	return diskCIDs, nil
}

func (d *Disks) CreateDisk(size int,
	cloudProps apiv1.DiskCloudProps, associatedVMCID *apiv1.VMCID) (apiv1.DiskCID, error) {
	var prop Property
	if err := cloudProps.As(&prop); err != nil {
		return apiv1.DiskCID{}, fmt.Errorf("unmarshalling cloud properties: %w", err)
	}
	if err := prop.validate(); err != nil {
		return apiv1.DiskCID{}, fmt.Errorf("validating disk cloud properties: %w", err)
	}

	ctx := context.Background()

	if size <= 0 {
		return apiv1.DiskCID{}, fmt.Errorf("disk size:%d cannot be equal to or less than zero", size)
	}
	sizeQuantity := *resource.NewQuantity(int64(size*unit.MiB), resource.BinarySI)

	diskUUID, err := d.getUUID()
	if err != nil {
		return apiv1.DiskCID{}, fmt.Errorf("generate UUID: %w", err)
	}
	diskName := fmt.Sprintf("disk-%s", diskUUID)

	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: v1.ObjectMeta{
			Name:      diskName,
			Namespace: d.projectID,
		},
		Spec: vmv1.VirtualMachineDiskSpec{
			Size: sizeQuantity,
			Type: vmv1.DiskType(prop.DiskType),
		},
	}

	err = d.cloudClient.Create(ctx, disk)
	if err != nil {
		return apiv1.DiskCID{}, err
	}

	diskReadyChecker := func(obj client.Object) (bool, error) {
		disk, ok := obj.(*vmv1.VirtualMachineDisk)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachineDisk: %v", obj)
		}
		return disk.Status.Phase == vmv1.DiskPhaseSucceeded, nil
	}

	if err := d.waiter.Until(ctx, diskReadyChecker, disk.Name, &vmv1.VirtualMachineDisk{}, time.Duration(d.timeout.DiskCreation)*time.Minute); err != nil {
		deleteErr := d.DeleteDisk(apiv1.NewDiskCID(diskName))
		if deleteErr != nil {
			return apiv1.DiskCID{}, fmt.Errorf("create disk fail: %w, clean up success", err)
		} else {
			return apiv1.DiskCID{}, fmt.Errorf("create disk fail: %w, clean up fail: %w", err, deleteErr)
		}
	}
	return apiv1.NewDiskCID(disk.Name), nil
}

func (d *Disks) DeleteDisk(cid apiv1.DiskCID) error {
	ctx := context.Background()
	return d.vmService.DeleteDisk(ctx, cid.AsString())
}

func (d *Disks) AttachDisk(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) error {
	_, err := d.AttachDiskV2(vmCID, diskCID)
	return err
}

func (d *Disks) AttachDiskV2(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) (apiv1.DiskHint, error) {
	ctx := context.Background()

	vm, err := d.finder.FindVM(ctx, vmCID.AsString())
	if err != nil {
		return apiv1.DiskHint{}, fmt.Errorf("find vm: %w", err)
	}

	if err := validateVMStateForAttachDetach(vm, diskOperationAttach, diskCID); err != nil {
		return apiv1.DiskHint{}, err
	}

	disk, err := d.finder.FindDisk(ctx, diskCID.AsString())
	if err != nil {
		return apiv1.DiskHint{}, fmt.Errorf("find disk: %w", err)
	}

	// explanation for why this specific path in go/bosh-gdch-disk-attach
	// tl;dr its the most stable udev rule at the moment.
	diskPath := fmt.Sprintf("/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_%s", diskCID.AsString())
	diskHint := apiv1.NewDiskHintFromString(diskPath)
	for _, da := range vm.Spec.Disks {
		if da.VirtualMachineDiskRef.Name == diskCID.AsString() {
			d.logger.Warn("AttachDisk", "bosh attempted to attach disk %s to vm %s, but it was already attached", diskCID.AsString(), vmCID.AsString())
			return diskHint, nil
		}
	}

	// Ensure the required VM IP exists and fail earlier if not to avoid unnecessary VM operations.
	vmIngressIP, err := d.findVMIP(ctx, vm.Name)
	if err != nil {
		return apiv1.DiskHint{}, fmt.Errorf("get vm ingress ip: %w", err)
	}
	stopped := isVMStopped(vm)
	var startedExplicitly bool

	// If disk hotplug is disabled and the VM is currently running, we must stop the VM
	// to perform a cold attach. We also defer a call to restart the VM (using a closure
	// to capture the latest VM state) to ensure the VM is restored to its original running
	// state even if the attach operation encounters an error. The startedExplicitly flag
	// prevents double-starts if the VM is explicitly started later for SSH operations.
	if !d.useDiskHotplug {
		if !stopped {
			if err := d.vmService.Stop(ctx, vm); err != nil {
				return apiv1.DiskHint{}, fmt.Errorf("stop VM %s: %w", vm.Name, err)
			}
			defer func() {
				if !startedExplicitly {
					_ = d.vmService.Start(ctx, vm)
				}
			}()

			// Refetch VM as vm state changed
			vm, err = d.finder.FindVM(ctx, vmCID.AsString())
			if err != nil {
				return apiv1.DiskHint{}, fmt.Errorf("find vm: %w", err)
			}
		}
	}

	vm.Spec.Disks = append(vm.Spec.Disks, vmv1.DiskAttachment{
		VirtualMachineDiskRef: corev1.LocalObjectReference{Name: disk.ObjectMeta.Name},
		Boot:                  ptr.To(false),
		AutoDelete:            ptr.To(false),
	})

	if err := d.cloudClient.Update(ctx, vm); err != nil {
		if apierrors.IsConflict(err) {
			return apiv1.DiskHint{}, bosherr.Err{
				Message:   fmt.Errorf("vm update: %w", err),
				Retryable: true,
			}
		}

		return apiv1.DiskHint{}, fmt.Errorf("vm update: %w", err)
	}

	// VirtualMachine object contains the Status field that helps to identify the
	// disk attachments to a corresponding VM that is running and available only for
	// hotplug disks. So for a VM that is running, we check for the disk attachment to be present in
	// the attachments list in the VirtualMachineStatus and the disk phase set to
	// `AttachedDiskPhaseReady. For a stopped VM we consider the success of
	// d.cloudClient.Update(..) to be sufficient.
	running := isVMRunning(vm)
	if d.useDiskHotplug {
		if running {
			if err := d.waitForHotDiskAttachment(ctx, vm.Name, diskCID.AsString()); err != nil {
				return apiv1.DiskHint{}, fmt.Errorf("wait for disk attachment: %w", err)
			}
		}
	}

	currentlyStopped := isVMStopped(vm)
	if currentlyStopped {
		// If we are not using the metadata server, updating the Agent Environment requires
		// an active SSH connection to the VM. Since the VM was stopped to attach the disk,
		// we must explicitly start it here before proceeding.
		// We set startedExplicitly to true so the deferred restart function (defined above)
		// knows it doesn't need to start the VM a second time.
		if !d.useMetadataServer {
			if err := d.vmService.Start(ctx, vm); err != nil {
				return apiv1.DiskHint{}, fmt.Errorf("start vm %s: %w", vm.Name, err)
			}
			startedExplicitly = true
			if stopped {
				defer d.stopVM(ctx, vm.Name)
			}
		}
	}

	var agentEnv apiv1.AgentEnv
	if d.useMetadataServer {
		agentEnv, err = d.metadataClient.Fetch(ctx, vm.Name)
		if err != nil {
			return apiv1.DiskHint{}, fmt.Errorf("fetch agent env: %w", err)
		}
	} else {
		agentEnv, err = d.agentEnvService.Fetch(ctx, vm.Name, vmIngressIP)
		if err != nil {
			return apiv1.DiskHint{}, fmt.Errorf("fetch agent env: %w", err)
		}
	}

	agentEnv.AttachPersistentDisk(diskCID, diskHint)

	if d.useMetadataServer {
		if err := d.metadataClient.Update(ctx, vmCID.AsString(), agentEnv); err != nil {
			return apiv1.DiskHint{}, fmt.Errorf("update agent env: %w", err)
		}
	} else {
		if err := d.agentEnvService.Update(ctx, vmCID.AsString(), vmIngressIP, agentEnv); err != nil {
			return apiv1.DiskHint{}, fmt.Errorf("update agent env: %w", err)
		}
	}

	return diskHint, nil
}

func (d *Disks) DetachDisk(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) error {
	ctx := context.Background()

	vm, err := d.finder.FindVM(ctx, vmCID.AsString())
	if err != nil {
		return err
	}

	if err := validateVMStateForAttachDetach(vm, diskOperationDetach, diskCID); err != nil {
		return err
	}

	var newDisks []vmv1.DiskAttachment
	var diskWasDetached bool
	for _, diskAttachment := range vm.Spec.Disks {
		if diskAttachment.VirtualMachineDiskRef.Name == diskCID.AsString() {
			diskWasDetached = true
		} else {
			newDisks = append(newDisks, diskAttachment)
		}
	}

	if !diskWasDetached {
		d.logger.Warn("DetachDisk", "bosh attempted to detach disk %s from vm %s, but it was already detached", diskCID.AsString(), vmCID.AsString())
		return nil
	}

	// Ensure the required VM IP exists and fail earlier if not to avoid unnecessary VM operations.
	vmIngressIP, err := d.findVMIP(ctx, vm.Name)
	if err != nil {
		return fmt.Errorf("get vm ingress ip: %w", err)
	}

	stopped := isVMStopped(vm)
	var startedExplicitly bool

	// If disk hotplug is disabled and the VM is currently running, we must stop the VM
	// to perform a cold detach. We also defer a call to restart the VM (using a closure
	// to capture the latest VM state) to ensure the VM is restored to its original running
	// state even if the detach operation encounters an error. The startedExplicitly flag
	// prevents double-starts if the VM is explicitly started later for SSH operations.
	if !d.useDiskHotplug {
		if !stopped {
			if err := d.vmService.Stop(ctx, vm); err != nil {
				return fmt.Errorf("stop VM %s: %w", vm.Name, err)
			}
			defer func() {
				if !startedExplicitly {
					_ = d.vmService.Start(ctx, vm)
				}
			}()
			// Refetch VM as vm state has changed
			vm, err = d.finder.FindVM(ctx, vmCID.AsString())
			if err != nil {
				return fmt.Errorf("find vm: %w", err)
			}
		}
	}

	vm.Spec.Disks = newDisks
	if err := d.cloudClient.Update(ctx, vm); err != nil {
		if apierrors.IsConflict(err) {
			return bosherr.Err{
				Message:   fmt.Errorf("vm update: %w", err),
				Retryable: true,
			}
		}

		return fmt.Errorf("vm update: %w", err)
	}

	// VirtualMachine object contains the Status field that helps to identify the
	// disk attachments to a corresponding VM that is running and available only for
	// hotplug disks. So for a VM that is running, we check for the disk detachment
	// to be successful, we see if the attachments list in the VirtualMachineStatus
	// does not contain the disk and we consider it detached. For a stopped VM we consider
	// the success of d.cloudClient.Update(..) to be sufficient.
	running := isVMRunning(vm)
	if d.useDiskHotplug {
		if running {
			if err := d.waitForHotDiskDetachment(ctx, vm.Name, diskCID.AsString()); err != nil {
				return fmt.Errorf("wait for disk detachment: %w", err)
			}
		}
	}

	currentlyStopped := isVMStopped(vm)
	if currentlyStopped {
		// If we are not using the metadata server, updating the Agent Environment requires
		// an active SSH connection to the VM. Since the VM was stopped to attach the disk,
		// we must explicitly start it here before proceeding.
		// We set startedExplicitly to true so the deferred restart function (defined above)
		// knows it doesn't need to start the VM a second time.
		if !d.useMetadataServer {
			if err := d.vmService.Start(ctx, vm); err != nil {
				return fmt.Errorf("start vm %s: %w", vm.Name, err)
			}
			startedExplicitly = true
			if stopped {
				defer d.stopVM(ctx, vm.Name)
			}
		}
	}

	var agentEnv apiv1.AgentEnv
	if d.useMetadataServer {
		agentEnv, err = d.metadataClient.Fetch(ctx, vm.Name)
		if err != nil {
			return fmt.Errorf("fetch agent env: %w", err)
		}
	} else {
		agentEnv, err = d.agentEnvService.Fetch(ctx, vm.Name, vmIngressIP)
		if err != nil {
			return fmt.Errorf("fetch agent env: %w", err)
		}
	}

	agentEnv.DetachPersistentDisk(diskCID)

	if d.useMetadataServer {
		if err := d.metadataClient.Update(ctx, vmCID.AsString(), agentEnv); err != nil {
			return fmt.Errorf("update agent env: %w", err)
		}
	} else {
		if err := d.agentEnvService.Update(ctx, vmCID.AsString(), vmIngressIP, agentEnv); err != nil {
			return fmt.Errorf("update agent env: %w", err)
		}
	}

	return nil
}

func (d *Disks) HasDisk(cid apiv1.DiskCID) (bool, error) {
	return d.finder.HasDisk(context.Background(), cid.AsString())
}

func (d *Disks) SetDiskMetadata(cid apiv1.DiskCID, metadata apiv1.DiskMeta) error {
	ctx := context.Background()
	disk, err := d.finder.FindDisk(ctx, cid.AsString())
	if err != nil {
		return fmt.Errorf("find disk: %w", err)
	}

	metaJson, err := metadata.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	metadataMap := make(map[string]string)
	if err := json.Unmarshal(metaJson, &metadataMap); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	// Set metadata with length violations against kubernetes labels to kubernetes annotations
	filteredMetadataMap := util.GetMetadataWithLengthViolation(metadataMap)
	if err := d.setDiskMetadataInAnnotations(ctx, disk, filteredMetadataMap); err != nil {
		return fmt.Errorf("update disk %s with metadata: %w", disk.Name, err)
	}

	// Patch BOSH set disk metadata which can have characters kubernetes labels doesn't accept, e.g.,
	// https://github.com/cloudfoundry/bosh-cli/blob/main/deployment/vm/vm.go#L329.
	util.PatchMetadata(metadataMap, d.logger)

	if disk.ObjectMeta.Labels == nil {
		disk.ObjectMeta.Labels = make(map[string]string)
	}

	labels := disk.ObjectMeta.Labels
	oldLabels := make(map[string]string)
	for k, v := range labels {
		oldLabels[k] = v
	}

	for k, v := range metadataMap {
		if _, exists := disk.ObjectMeta.Labels[k]; exists {
			d.logger.Warn("SetDiskMetadata", "annotation key %q was overwritten", k)
		}
		labels[k] = v
	}

	if !reflect.DeepEqual(oldLabels, disk.ObjectMeta.Labels) {
		if err = d.cloudClient.Update(ctx, disk); err != nil {
			return fmt.Errorf("update disk %s with metadata: %w", disk.Name, err)
		}
	} else {
		d.logger.Debug("SetDiskMetadata", "Metadata is unchanged; skipping update")
	}
	return nil
}

func (d *Disks) setDiskMetadataInAnnotations(ctx context.Context, disk *vmv1.VirtualMachineDisk, matadataMap map[string]string) error {
	if len(matadataMap) == 0 {
		return nil
	}

	if disk.ObjectMeta.Annotations == nil {
		disk.ObjectMeta.Annotations = make(map[string]string)
	}

	annotations := disk.ObjectMeta.Annotations
	// Copy annotations for comparison later
	oldAnnotations := make(map[string]string)
	for k, v := range annotations {
		oldAnnotations[k] = v
	}

	for k, val := range matadataMap {
		if _, exists := disk.ObjectMeta.Annotations[k]; exists {
			d.logger.Warn("setVMMetaData", "key %s was overwritten in annotations", k)
		}
		annotations[k] = val
	}

	if !reflect.DeepEqual(oldAnnotations, disk.ObjectMeta.Annotations) {
		if err := d.cloudClient.Update(ctx, disk); err != nil {
			return fmt.Errorf("update disk %s with metadata: %w", disk.Name, err)
		}
	} else {
		d.logger.Debug("setVMMetaData", "Not updating metadata in annotations as it is identical as the existing setting")
	}
	return nil
}

func (d *Disks) ResizeDisk(cid apiv1.DiskCID, size int) error {
	ctx := context.Background()

	sizeQuantity := *resource.NewQuantity(int64(size*unit.MiB), resource.BinarySI)

	disk, err := d.finder.FindDisk(ctx, cid.AsString())
	if err != nil {
		return fmt.Errorf("find disk: %w", err)
	}

	if !d.enableHotDiskExpansion && disk.Status.VirtualMachineAttachments != nil && len(disk.Status.VirtualMachineAttachments) > 0 {
		return fmt.Errorf("Disk %q still has attached VMs, resize is not supported", cid.AsString())
	}

	patch := client.MergeFrom(disk.DeepCopy())
	disk.Spec.Size = sizeQuantity
	if err = d.cloudClient.Patch(ctx, disk, patch); err != nil {
		return err
	}

	diskResizeChecker := func(obj client.Object) (bool, error) {
		disk, ok := obj.(*vmv1.VirtualMachineDisk)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachineDisk: %v", obj)
		}

		return disk.Status.Size.Cmp(sizeQuantity) == 0, nil
	}

	if err := d.waiter.Until(ctx, diskResizeChecker, disk.Name, &vmv1.VirtualMachineDisk{}, time.Duration(d.timeout.DiskResize)*time.Minute); err != nil {
		return bosherr.Err{
			Message:   err,
			ErrType:   bosherr.BoshErrCloudErr,
			Retryable: false,
		}
	}

	return nil
}

func (d *Disks) SnapshotDisk(cid apiv1.DiskCID, meta apiv1.DiskMeta) (apiv1.SnapshotCID, error) {
	ctx := context.Background()
	if _, err := d.finder.FindDisk(ctx, cid.AsString()); err != nil {
		return apiv1.SnapshotCID{}, fmt.Errorf("find disk: %w", err)
	}

	backupName := fmt.Sprintf("backup-%s-%d", cid.AsString(), d.getTimestamp())

	backupRequest := &vmv1.VirtualMachineBackupRequest{
		ObjectMeta: v1.ObjectMeta{
			Name:      fmt.Sprintf("%s-req", backupName),
			Namespace: d.projectID,
		},
		Spec: vmv1.VirtualMachineBackupRequestSpec{
			VirtualMachineBackupPlanTemplate: diskSnapshotBackupPlanTemplate,
			VirtualMachineDisk:               cid.AsString(),
			VirtualMachineBackupName:         backupName,
		},
	}

	if err := d.cloudClient.Create(ctx, backupRequest); err != nil {
		return apiv1.SnapshotCID{}, fmt.Errorf("create backup request: %w", err)
	}

	requestReadyChecker := func(obj client.Object) (bool, error) {
		backup, ok := obj.(*vmv1.VirtualMachineBackup)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachineBackup: %v", obj)
		}
		if backup.Status.BackupStatus.State == backupv1.BackupStateFailed {
			return false, fmt.Errorf("backup %s failed", backup.Name)
		}
		return backup.Status.BackupStatus.State == backupv1.BackupStateSucceeded, nil
	}

	if err := d.waiter.Until(
		ctx,
		requestReadyChecker,
		backupRequest.Spec.VirtualMachineBackupName,
		&vmv1.VirtualMachineBackup{},
		time.Duration(d.timeout.SnapshotCreation)*time.Minute,
	); err != nil {
		return apiv1.SnapshotCID{}, fmt.Errorf("wait until backup %s becomes ready: %w", backupRequest.Spec.VirtualMachineBackupName, err)
	}

	return apiv1.NewSnapshotCID(backupName), nil
}

func (d *Disks) DeleteSnapshot(cid apiv1.SnapshotCID) error {
	backupName := cid.AsString()

	ctx := context.Background()
	exists, err := d.hasBackup(ctx, backupName)
	if err != nil {
		return fmt.Errorf("search backup: %w", err)
	}
	if !exists {
		d.logger.Info("disk", "Snapshot %s does not exist, skipping deletion", backupName)
		return nil
	}

	deleteRequest := &vmv1.VirtualMachineDeleteBackupRequest{
		ObjectMeta: v1.ObjectMeta{
			Name:      fmt.Sprintf("%s-delete-req", backupName),
			Namespace: d.projectID,
		},
		Spec: vmv1.VirtualMachineDeleteBackupRequestSpec{
			VirtualMachineBackupRef: corev1.LocalObjectReference{
				Name: backupName,
			},
		},
	}

	if err := d.cloudClient.Create(ctx, deleteRequest); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create delete backup request: %w", err)
	}

	if err := d.waiter.UntilDeleted(ctx, backupName, &vmv1.VirtualMachineBackup{}, time.Duration(d.timeout.SnapshotDeletion)*time.Minute); err != nil {
		return fmt.Errorf("wait until disk backup %q is deleted: %w", backupName, err)
	}

	return nil
}

func (d *Disks) getUUID() (string, error) {
	genUUID, err := d.uuidGen.Generate()
	if err != nil {
		return "", err
	}
	return strings.Split(genUUID, "-")[0], nil
}

func (d *Disks) hasBackup(ctx context.Context, backupName string) (bool, error) {
	backup := &vmv1.VirtualMachineBackup{}
	if err := d.cloudClient.Get(ctx, client.ObjectKey{Name: backupName, Namespace: d.projectID}, backup); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		} else {
			return false, fmt.Errorf("get backup %q: %w", backupName, err)
		}
	}
	return true, nil
}

func (d *Disks) getTimestamp() int64 {
	return d.clock.Now().Unix()
}

func (d *Disks) waitForHotDiskAttachment(ctx context.Context, vmName, diskName string) error {
	diskAttachmentReadyChecker := func(obj client.Object) (bool, error) {
		vm, ok := obj.(*vmv1.VirtualMachine)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachine: %v", obj)
		}
		if vm.Status.Disks == nil {
			return false, nil
		}
		for _, diskAttachment := range vm.Status.Disks.Attachments {
			if diskAttachment.Hotpluggable != nil && *diskAttachment.Hotpluggable && diskAttachment.Name == diskName && diskAttachment.Phase == vmv1.AttachedDiskPhaseReady {
				return true, nil
			}
		}
		return false, nil
	}
	return d.waiter.Until(ctx, diskAttachmentReadyChecker, vmName, &vmv1.VirtualMachine{}, time.Duration(d.timeout.HotDiskAttachment)*time.Minute)
}

func (d *Disks) waitForHotDiskDetachment(ctx context.Context, vmName, diskName string) error {
	diskDetachmentChecker := func(obj client.Object) (bool, error) {
		vm, ok := obj.(*vmv1.VirtualMachine)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachine: %v", obj)
		}
		if vm.Status.Disks == nil {
			return true, nil
		}
		for _, diskAttachment := range vm.Status.Disks.Attachments {
			if diskAttachment.Hotpluggable != nil && *diskAttachment.Hotpluggable && diskAttachment.Name == diskName {
				return false, nil
			}
		}

		return true, nil
	}
	return d.waiter.Until(ctx, diskDetachmentChecker, vmName, &vmv1.VirtualMachine{}, time.Duration(d.timeout.HotDiskDetachment)*time.Minute)
}

func (d *Disks) stopVM(ctx context.Context, vmName string) {
	vm, err := d.finder.FindVM(ctx, vmName)
	if err != nil {
		d.logger.Warn("disk", "find VM %q: %v", vmName, err)
		return
	}

	if err := d.vmService.Stop(ctx, vm); err != nil {
		d.logger.Warn("disk", "stop VM %q: %v", vmName, err)
	}
}

func isVMStopped(vm *vmv1.VirtualMachine) bool {
	return vm.Status.State == vmv1.VirtualMachineStateStopped
}

func isVMRunning(vm *vmv1.VirtualMachine) bool {
	return vm.Status.State == vmv1.VirtualMachineStateRunning
}

func validateVMStateForAttachDetach(vm *vmv1.VirtualMachine, op string, diskCID apiv1.DiskCID) error {
	if !isVMRunning(vm) && !isVMStopped(vm) {
		return fmt.Errorf("VM %s (State: %s) is not in a valid state (running or stopped) to %s disk %s", vm.Name, vm.Status.State, op, diskCID.AsString())
	}
	return nil
}

func (d *Disks) findVMIP(ctx context.Context, vmName string) (string, error) {
	// Try with ingress IP first; if not found, use internal IP
	vmIngressIP, err := d.finder.FindVMIngressIP(ctx, vmName)

	if err == nil {
		d.logger.Debug("disk", "find vm %s ingress ip %s", vmName, vmIngressIP)
		return vmIngressIP, nil
	} else if !apierrors.IsNotFound(err) {
		return "", err
	}

	vmIP, err := d.finder.FindVMIP(ctx, vmName)
	if err == nil {
		d.logger.Debug("disk", "find vm %s ip %s", vmName, vmIP)
	}
	return vmIP, err
}

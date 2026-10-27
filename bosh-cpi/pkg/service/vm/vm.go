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
	"time"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

type waiter interface {
	Until(ctx context.Context, check wait.ConditionChecker, name string, obj client.Object, timeout time.Duration) error
	UntilDeleted(ctx context.Context, name string, obj client.Object, timeout time.Duration) error
}

type VMService struct {
	Client    client.Client
	ProjectID string
	Waiter    waiter
	Logger    cfboshlog.Logger
	Timeout   *config.WaitTimeout
}

// Stop changes the desired state of the VM to
// `VirtualMachineRunningStateStopped`, and waits until the actual state of the
// VM is "Stopped" state with the status of `Editable“ condition set to true.
func (v *VMService) Stop(ctx context.Context, vm *vmv1.VirtualMachine) error {
	if err := v.changeVMRunningState(ctx, vm, vmv1.VirtualMachineRunningStateStopped); err != nil {
		return fmt.Errorf("stop VM %s: %w", vm.Name, err)
	}

	if err := v.WaitForVMState(ctx, vm.Name, vmv1.VirtualMachineStateStopped, vmv1.ConditionTypeEditable, metav1.ConditionTrue, time.Duration(v.Timeout.VMStop)*time.Minute); err != nil {
		return fmt.Errorf("wait for VM %s to stop: %w", vm.Name, err)
	}

	return nil
}

// Start sets the desired state of the VM to
// `VirtualMachineRunningStateRunning`, and waits until the actual state of the
// VM is "Running" state with the status of `GuestEnvironmentSynced` condition set to true.
func (v *VMService) Start(ctx context.Context, vm *vmv1.VirtualMachine) error {
	if err := v.changeVMRunningState(ctx, vm, vmv1.VirtualMachineRunningStateRunning); err != nil {
		return fmt.Errorf("start VM %s: %w", vm.Name, err)
	}

	if err := v.WaitForVMState(ctx, vm.Name, vmv1.VirtualMachineStateRunning, vmv1.ConditionTypeGuestEnvironmentSynced, metav1.ConditionTrue, time.Duration(v.Timeout.VMStart)*time.Minute); err != nil {
		return fmt.Errorf("wait for VM %s to be running: %w", vm.Name, err)
	}

	return nil
}

func (v *VMService) DeleteDisk(ctx context.Context, diskName string) error {
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diskName,
			Namespace: v.ProjectID,
		},
	}

	if err := v.Client.Delete(ctx, disk); apierrors.IsNotFound(err) {
		v.Logger.Debug("DeleteDisk", "disk %q does not exist, no-op", diskName)
		return nil
	} else if err != nil {
		return fmt.Errorf("delete disk %q: %w", diskName, err)
	}

	if err := v.Waiter.UntilDeleted(ctx, disk.Name, &vmv1.VirtualMachineDisk{}, time.Duration(v.Timeout.DiskDeletion)*time.Minute); err != nil {
		return fmt.Errorf("wait until disk %q is deleted: %w", disk.Name, err)
	}

	return nil
}

// Wait util vm state is the desired VM state.
func (v *VMService) WaitForVMState(
	ctx context.Context,
	name string,
	state vmv1.VirtualMachineState,
	conditionType string,
	conditionTypeStatus metav1.ConditionStatus,
	timeout time.Duration,
) error {
	vmStatusChecker := func(obj client.Object) (bool, error) {
		vm, ok := obj.(*vmv1.VirtualMachine)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachineInstance: %v", obj)
		}

		// terminate the wait if VM is unschedulable
		if vm.Status.State == vmv1.VirtualMachineStateUnschedulable {
			return false, fmt.Errorf("VM %s was unschedulable", name)
		}

		return vm.Status.State == state && meta.IsStatusConditionPresentAndEqual(vm.Status.Conditions, conditionType, conditionTypeStatus), nil
	}

	if err := v.Waiter.Until(ctx, vmStatusChecker, name, &vmv1.VirtualMachine{}, timeout); err != nil {
		vm := &vmv1.VirtualMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: v.ProjectID,
			},
		}

		if err := v.Client.Get(ctx, client.ObjectKeyFromObject(vm), vm); apierrors.IsNotFound(err) {
			return nil
		} else if err != nil {
			return fmt.Errorf("get VM %s: %w", name, err)
		}

		error := fmt.Errorf("wait until VM %s state is %q and condition type %q is %q: %w.\nLatest status:\n State: %q", name, state, conditionType, conditionTypeStatus, err, vm.Status.State)
		if len(vm.Status.Reason) > 0 {
			error = fmt.Errorf("%s\n Reason: %q", error.Error(), vm.Status.Reason)
		}
		if len(vm.Status.Message) > 0 {
			error = fmt.Errorf("%s\n Message: %q", error.Error(), vm.Status.Message)
		}

		return error
	}
	return nil
}

// Delete deletes a VirtualMachine object by the given name. A nil error is
// returned immediately if the VM object is non-existent, otherwise the method
// is blocked until the VM CR as well as attached disk CRs with `AutoDelete`
// turned on are fully deleted.
func (v *VMService) Delete(ctx context.Context, vmName string) error {
	vm := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: v.ProjectID,
		},
	}

	if err := v.Client.Get(ctx, client.ObjectKeyFromObject(vm), vm); apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("get VM %s/%s: %w", v.ProjectID, vmName, err)
	}

	var disksToDelete []string
	for _, disk := range vm.Spec.Disks {
		if *disk.AutoDelete {
			disksToDelete = append(disksToDelete, disk.VirtualMachineDiskRef.Name)
		}
	}

	if err := v.Client.Delete(ctx, vm); err != nil {
		return fmt.Errorf("delete VM %s/%s: %w", v.ProjectID, vmName, err)
	}

	if err := v.Waiter.UntilDeleted(ctx, vmName, &vmv1.VirtualMachine{},
		time.Duration(v.Timeout.VMDeletion)*time.Minute); err != nil {
		return fmt.Errorf("wait until VM %q is deleted: %w", vm.Name, err)
	}

	// Until VMM supports (b/369855189), CPI will force disk deletions since there is currently a known delay
	// from the VMM controller side between VM deletion and the deletion of associated disks configured with
	// auto deletion. This won't cause a race condition as `v.DeleteDisk`` swallows NotFound errors
	for _, diskName := range disksToDelete {
		if err := v.DeleteDisk(ctx, diskName); err != nil {
			return fmt.Errorf("confirm VM disk %q is deleted: %w", diskName, err)
		}
	}

	return nil
}

func (v *VMService) changeVMRunningState(ctx context.Context, vm *vmv1.VirtualMachine, st vmv1.VirtualMachineRunningState) error {
	if err := v.Client.Get(ctx, client.ObjectKeyFromObject(vm), vm); err != nil {
		return fmt.Errorf("unable to get VM: %w", err)
	}

	patchHelper, err := patch.NewHelper(vm, v.Client)
	if err != nil {
		return fmt.Errorf("failed to initialize patch helper: %w", err)
	}

	vm.Spec.RunningState = &st
	if err := patchHelper.Patch(ctx, vm); err != nil {
		return fmt.Errorf("patch VM: %w", err)
	}
	return nil
}

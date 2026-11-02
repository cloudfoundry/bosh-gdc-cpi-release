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
	"fmt"
	"reflect"
	"time"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"github.com/hashicorp/go-multierror"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/network"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/unit"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/util"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

type vmCreationInput struct {
	agentID           apiv1.AgentID
	vmName            string
	prop              Property
	bootDiskName      string
	ephemeralDiskName string
	networks          apiv1.Networks
	env               apiv1.VMEnv
	ip                string
}

const (
	metadataAgentConfigScriptName = "configure-bosh-agent-metadata"
	metadataAgentConfigScript     = `#!/bin/bash
if [ -f /var/vcap/bosh/agent.json ] && ! grep -q '"InstanceMetadata"' /var/vcap/bosh/agent.json; then
  cat <<'EOF' > /var/vcap/bosh/agent.json
{
  "Platform": {
    "Linux": {
      "CreatePartitionIfNoEphemeralDisk": true,
      "PartitionerType": "parted",
      "DevicePathResolutionType": "virtio",
      "VirtioDevicePrefix": "gdch",
      "ServiceManager": "systemd"
    }
  },
  "Infrastructure": {
    "Settings": {
      "Sources": [
        {
          "Type": "InstanceMetadata",
          "URI": "http://169.254.169.254",
          "SettingsPath": "/computeMetadata/v1/instance/attributes/bosh-settings",
          "Headers": {
            "Metadata-Flavor": "Google"
          }
        }
      ],
      "UseServerName": true,
      "UseRegistry": false
    }
  }
}
EOF
  mkdir -p /etc/systemd/system/bosh-agent.service.d
  printf "[Unit]\nStartLimitBurst=60\n[Service]\nRestartSec=10\n" > /etc/systemd/system/bosh-agent.service.d/override.conf
  systemctl daemon-reload || true
  systemctl restart bosh-agent || true
fi
`
)

func (v *VMs) setVMMetadata(ctx context.Context, vm *vmv1.VirtualMachine, metadata apiv1.VMMeta) error {
	jsonData, err := metadata.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling VM metadata: %w", err)
	}

	metadataMap := make(map[string]string)
	err = json.Unmarshal(jsonData, &metadataMap)
	if err != nil {
		return fmt.Errorf("unmarshaling VM metadata to key value map: %w", err)
	}

	// Set metadata with length violations against kubernetes labels to kubernetes annotations
	filteredMetadataMap := util.GetMetadataWithLengthViolation(metadataMap)
	if err := v.setVMMetadataInAnnotations(ctx, vm, filteredMetadataMap); err != nil {
		return fmt.Errorf("update VM %s with metadata: %w", vm.Name, err)
	}

	// Patch BOSH set vm metadata which can have characters kubernetes labels doesn't accept, e.g.,
	// https://github.com/cloudfoundry/bosh-cli/blob/main/deployment/vm/manager.go#L120-L128.
	util.PatchMetadata(metadataMap, v.logger)

	if vm.ObjectMeta.Labels == nil {
		vm.ObjectMeta.Labels = make(map[string]string)
	}

	labels := vm.ObjectMeta.Labels
	// Copy labels for comparison later
	oldLabels := make(map[string]string)
	for k, v := range labels {
		oldLabels[k] = v
	}

	for k, val := range metadataMap {
		if _, exists := vm.ObjectMeta.Labels[k]; exists {
			v.logger.Warn("setVMMetaData", "key %s was overwritten in labels", k)
		}
		labels[k] = val
	}

	if !reflect.DeepEqual(oldLabels, vm.ObjectMeta.Labels) {
		if err = v.cloudClient.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM %s with metadata: %w", vm.Name, err)
		}
	} else {
		v.logger.Debug("setVMMetaData", "Not updating metadata as it is identical as the existing setting")
	}

	return nil
}

func (v *VMs) setVMMetadataInAnnotations(ctx context.Context, vm *vmv1.VirtualMachine, metadataMap map[string]string) error {
	if len(metadataMap) == 0 {
		return nil
	}

	if vm.ObjectMeta.Annotations == nil {
		vm.ObjectMeta.Annotations = make(map[string]string)
	}

	annotations := vm.ObjectMeta.Annotations
	// Copy annotations for comparison later
	oldAnnotations := make(map[string]string)
	for k, v := range annotations {
		oldAnnotations[k] = v
	}

	for k, val := range metadataMap {
		if _, exists := vm.ObjectMeta.Annotations[k]; exists {
			v.logger.Warn("setVMMetaData", "key %s was overwritten in annotations", k)
		}
		annotations[k] = val
	}

	if !reflect.DeepEqual(oldAnnotations, vm.ObjectMeta.Annotations) {
		if err := v.cloudClient.Update(ctx, vm); err != nil {
			return fmt.Errorf("update VM %s with metadata: %w", vm.Name, err)
		}
	} else {
		v.logger.Debug("setVMMetaData", "Not updating metadata in annotations as it is identical as the existing setting")
	}
	return nil
}

// PerformReboot reboots a VM regardless of if it is powered on or off at the time of the call:
// https://github.com/cloudfoundry/docs-bosh/blob/master/content/cpi-api-v2-method/reboot-vm.md
func (v *VMs) PerformReboot(ctx context.Context, vm *vmv1.VirtualMachine) error {
	vmState := vm.Status.State
	v.logger.Debug("PerformReboot", "Rebooting VM %s in state %s", vm.Name, vmState)
	switch vmState {
	case vmv1.VirtualMachineStateRunning:
		// VM shutdown has a default graceful period (30s). Reboot can be issued by
		// stopping the VM and followed with an immediate start.
		// https://github.com/kubevirt/kubevirt/blob/main/docs/graceful-shutdown.md
		if err := v.vmService.Stop(ctx, vm); err != nil {
			return fmt.Errorf("power off VM %s: %w", vm.Name, err)
		}
		if err := v.vmService.Start(ctx, vm); err != nil {
			return fmt.Errorf("power on VM %s: %w", vm.Name, err)
		}
		return nil
	case vmv1.VirtualMachineStateStopped:
		if err := v.vmService.Start(ctx, vm); err != nil {
			return fmt.Errorf("power on VM %s: %w", vm.Name, err)
		}
		return nil
	default:
		// Should not reboot VMs in an unstable state. It may cause corruption or other problems.
		return fmt.Errorf("can not reboot vm %s in state %s", vm.Name, vmState)
	}
}

func (v *VMs) validateDisks(ctx context.Context, diskIDs []apiv1.DiskCID) error {
	var errs *multierror.Error

	for _, id := range diskIDs {
		if _, err := v.finder.FindDisk(ctx, id.AsString()); err != nil {
			errs = multierror.Append(errs, err)
		}
	}

	return errs.ErrorOrNil()
}

func (v *VMs) createBootDisk(ctx context.Context, diskName, vmName string, stemcellID apiv1.StemcellCID, bootDiskGB int, bootDiskType string) error {
	id := stemcellID.AsString()
	image, err := v.imageService.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get image %q: %w", id, err)
	}

	diskSizeQuantity := resource.NewQuantity(int64(bootDiskGB*unit.GiB), resource.BinarySI)
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diskName,
			Namespace: v.projectID,
		},
		Spec: vmv1.VirtualMachineDiskSpec{
			Source: &vmv1.DiskSource{
				Image: &vmv1.ImageDiskSource{
					Name:      image.Name,
					Namespace: image.Namespace,
				},
			},
			Size: *diskSizeQuantity,
			Type: vmv1.DiskType(bootDiskType),
		},
	}

	if err := v.cloudClient.Create(ctx, disk); err != nil {
		return fmt.Errorf("create boot disk from stemcell %s: %w", stemcellID.AsString(), err)
	}

	if err := v.waitForDiskReady(ctx, diskName, time.Duration(v.timeout.DiskCreation)*time.Minute); err != nil {
		v.cleanupOrphans(ctx, vmName)
		return err
	}

	return nil
}

func (v *VMs) createEphemeralDisk(ctx context.Context, diskName, vmName string, ephemeralDiskGB int, ephemeralDiskType string) error {
	diskSizeQuantity := resource.NewQuantity(int64(ephemeralDiskGB)*unit.GiB, resource.BinarySI)
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      diskName,
			Namespace: v.projectID,
		},
		Spec: vmv1.VirtualMachineDiskSpec{
			Size: *diskSizeQuantity,
			Type: vmv1.DiskType(ephemeralDiskType),
		},
	}

	if err := v.cloudClient.Create(ctx, disk); err != nil {
		return fmt.Errorf("create ephemeral disk %q for BOSH agent in VM: %w", diskName, err)
	}

	if err := v.waitForDiskReady(ctx, diskName, time.Duration(v.timeout.DiskCreation)*time.Minute); err != nil {
		v.cleanupOrphans(ctx, vmName)
		return err
	}

	return nil
}

func (v *VMs) createVM(ctx context.Context, input vmCreationInput) error {
	var vcpus uint32
	var computeSpec vmv1.Compute
	if len(input.prop.VirtualMachineType) > 0 {
		v.logger.Info("createVM", "Use pre-defined machine type %q", input.prop.VirtualMachineType)

		foundVMType, err := v.vmService.GetVirtualMachineType(ctx, input.prop.VirtualMachineType)
		if err != nil {
			return err
		}

		v.logger.Info("createVM", "Pre-defined machine type %q has %d vCPUs", input.prop.VirtualMachineType, foundVMType.Spec.VCPUs)
		vcpus = foundVMType.Spec.VCPUs
		computeSpec = vmv1.Compute{
			VirtualMachineType: input.prop.VirtualMachineType,
		}
	} else {
		v.logger.Info("createVM", "Use custom VM spec with %v vCPUs and %vGB memory", input.prop.VCPUs, input.prop.MemorySizeGB)
		vcpus = uint32(input.prop.VCPUs)
		computeSpec = vmv1.Compute{
			VCPUs:  vcpus,
			Memory: resource.NewQuantity(int64(input.prop.MemorySizeGB*unit.GiB), resource.BinarySI),
		}
	}

	annotations := map[string]string{}
	if v.useDiskHotplug {
		annotations[consts.DiskHotPlugAnnotationKey] = "true"
	}

	vm := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:        input.vmName,
			Namespace:   v.projectID,
			Labels:      map[string]string{},
			Annotations: annotations,
		},
		Spec: vmv1.VirtualMachineSpec{
			Compute: computeSpec,
			Disks: []vmv1.DiskAttachment{
				{
					Boot:                  ptr.To(true),
					VirtualMachineDiskRef: corev1.LocalObjectReference{Name: input.bootDiskName},
					AutoDelete:            ptr.To(true),
				},
				{
					Boot:                  ptr.To(false),
					VirtualMachineDiskRef: corev1.LocalObjectReference{Name: input.ephemeralDiskName},
					AutoDelete:            ptr.To(true),
				},
			},
		},
	}

	labels := make(map[string]string)
	if input.prop.Labels != nil {
		for k, v := range input.prop.Labels {
			labels[k] = v
		}
	}
	// Add zone as label of the VM instance
	if len(input.prop.Zone) > 0 {
		v.logger.Info("createVM", "Add zone label 'zone: %s' to VM %q", input.prop.Zone, input.vmName)
		labels[consts.ZoneKey] = input.prop.Zone
	}
	vm.Labels = labels

	// Determine if Tier 1 should be enabled based on global config OR instance-group property.
	tier1NetworkEnabled := v.useTier1Network || input.prop.UseTier1Network
	if tier1NetworkEnabled {
		if v.useTier1Network {
			v.logger.Info("createVM", "Enabling Tier 1 networking for VM %q via global CPI configuration.", input.vmName)
		} else {
			v.logger.Info("createVM", "Enabling Tier 1 networking for VM %q via vm extension cloud property.", input.vmName)
		}
		if IsTier1NetworkSupported(input.prop.VirtualMachineType, vcpus) {
			if vm.Spec.Network == nil {
				vm.Spec.Network = &vmv1.NetworkSpec{}
			}
			vm.Spec.Network.NetworkPerformanceTier = consts.NetworkPerformanceTier1
		} else {
			return fmt.Errorf("VM with machine type %q and %d vCPUs is not eligible for Tier 1 networking", input.prop.VirtualMachineType, vcpus)
		}
	}

	if len(input.prop.SubnetName) > 0 && len(input.ip) > 0 {
		if vm.Spec.Network == nil {
			vm.Spec.Network = &vmv1.NetworkSpec{}
		}
		vm.Spec.Network.Interfaces = []vmv1.NetworkInterfaceSpec{
			{
				Network: "default",
				Subnet:  input.prop.SubnetName,
				IPAddresses: []vmv1.IPAddress{
					{
						Address: input.ip,
					},
				},
			},
		}
		v.logger.Info("createVM", "assigning IP %q to VM %q", input.ip, vm.Name)
	}

	// associate the load balancer to the VM by populating the LB's selector as VM's label
	lb := input.prop.LoadBalancer
	if len(lb.Name) > 0 {
		v.logger.Info("createVM", "populating the LB %q's selector as VM %q's label", lb.Name, vm.Name)

		switch vm.Labels[lb.Selector.Key] {
		case lb.Selector.Value:
			v.logger.Debug("createVM", "label with LB %q selector's key %q and value %q already exits for VM %q", lb.Name, lb.Selector.Key, lb.Selector.Value, vm.Name)
		case "":
			vm.Labels[lb.Selector.Key] = lb.Selector.Value
		default:
			return fmt.Errorf("VM %q has specified label conflicting with LB %q's selector", vm.Name, lb.Name)
		}
	}

	if v.useCloudNATGateways {
		v.logger.Info("createVM", "Cloud NAT Gateways are enabled. Omit the label: %s from the VM", consts.VMExternalEgressControlLabelKey)
		cn := input.prop.CloudNAT
		if len(cn.Name) > 0 {
			if existingValue, ok := vm.Labels[cn.Selector.Key]; ok && existingValue != cn.Selector.Value {
				v.logger.Info("createVM", "Overwriting existing Cloud Nat selector label on VM %q from %q:%q to %q:%q", vm.Name, cn.Selector.Key, existingValue, cn.Selector.Key, cn.Selector.Value)
			}
			v.logger.Info("createVM", "populating the CloudNAT %q's selector as VM %q's label", cn.Name, vm.Name)
			vm.Labels[cn.Selector.Key] = cn.Selector.Value
		} else {
			v.logger.Info("createVM", "Cloud NAT VM cloud property is not present but Cloud NAT is enabled. Not setting VM label.")
		}
	} else {
		v.logger.Info("Cloud NAT Gateways are disabled. Add label: %v:true to VM", consts.VMExternalEgressControlLabelKey)
		// label added for external egress (legacy): b/370383312#comment8
		// TODO(b/370823810): use `VirtualMachineExternalAccess` to control VM egress
		vm.Labels[consts.VMExternalEgressControlLabelKey] = "true"
	}

	if input.prop.AccessEnabled {
		vm.Spec.GuestEnvironment = &vmv1.GuestEnvironment{
			AccessManagement: &vmv1.AccessManagementConfig{
				Enable: input.prop.AccessEnabled,
			},
		}
	}

	if len(input.prop.Firmware.Type) > 0 {
		vm.Spec.Firmware = &vmv1.Firmware{
			Type:             input.prop.Firmware.Type,
			EnableSecureBoot: &input.prop.Firmware.EnableSecureBoot,
		}
	}

	if v.useMetadataServer {
		vm.Spec.StartupScripts = []vmv1.StartupScript{
			{
				Name:   metadataAgentConfigScriptName,
				Script: metadataAgentConfigScript,
			},
		}
	}

	if err := v.cloudClient.Create(ctx, vm); err != nil {
		return fmt.Errorf("create vm: %w", err)
	}

	// By BOSH CPI doc: https://github.com/cloudfoundry/docs-bosh/blob/master/content/cpi-api-v2-method/create-vm.md
	// Need to cleanup the associated resource if creation fails.
	if err := v.vmService.WaitForVMState(ctx, vm.Name, vmv1.VirtualMachineStateRunning, vmv1.ConditionTypeGuestEnvironmentSynced, metav1.ConditionTrue, time.Duration(v.timeout.VMStart)*time.Minute); err != nil {
		v.cleanupAll(ctx, vm)
		return fmt.Errorf("wait for vm with IP %q in running state: %w", input.ip, err)
	}

	var vmIngressIP string
	var err error
	if len(v.access.externalAccessPorts) > 0 {
		v.logger.Info("createVM", "external access for VM is required, creating VirtualMachineExternalAccess")
		if err := v.access.create(ctx, input.vmName, v.useCloudNATGateways); err != nil {
			v.cleanupAll(ctx, vm)
			return fmt.Errorf("create VM access: %w", err)
		}

		v.logger.Info("createVM", "finding VM external IP as ingress IP")
		vmIngressIP, err = v.finder.FindVMIngressIP(ctx, input.vmName)
	} else {
		v.logger.Info("createVM", "external access for VM is not required, finding VM IP as ingress IP")
		vmIngressIP, err = v.finder.FindVMIP(ctx, input.vmName)
	}

	if err != nil {
		v.cleanupAll(ctx, vm)
		return fmt.Errorf("get VM ingress IP: %w", err)
	}

	if len(input.prop.PublicKey) > 0 {
		v.logger.Info("createVM", "PublicKey cloud property is present, creating VirtualMachineAccessRequest")
		if err := v.vmAccessService.EnsureVMARExistAndReady(vm.Name, input.prop.PublicKey); err != nil {
			v.cleanupAll(ctx, vm)
			return fmt.Errorf("ensure vm access request exists and is ready for public key: %w", err)
		}
	}

	agentEnv := apiv1.NewAgentEnvFactory().ForVM(
		input.agentID, apiv1.NewVMCID(input.vmName), input.networks, input.env, v.agentOptions)
	// GDCH VMM uses `virtio-scsi` disks, and the dafault disk address bus is 0,
	// https://github.com/kubevirt/kubevirt/blob/main/pkg/virt-launcher/virtwrap/converter/converter.go#L170.
	// Hence, until VMM addresses b/320800880, per the discussion with VMM, `scsi-0QEMU_QEMU_HARDDISK_` is
	// the most reliable disk path prefix referencing to that attached disk on GDCH VMs
	ephemeralDiskHint := apiv1.NewDiskHintFromMap(
		map[string]interface{}{
			"path": fmt.Sprintf("/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_%s", input.ephemeralDiskName),
		},
	)
	agentEnv.AttachEphemeralDisk(ephemeralDiskHint)

	if v.useMetadataServer {
		if err := v.metadataClient.Update(ctx, input.vmName, agentEnv); err != nil {
			v.cleanupAll(ctx, vm)
			return fmt.Errorf("send agent setting to metadata server: %w", err)
		}
	} else {
		if err := v.agentEnvService.Update(ctx, input.vmName, vmIngressIP, agentEnv); err != nil {
			v.cleanupAll(ctx, vm)
			return fmt.Errorf("send agent setting to vm local path: %w", err)
		}
	}

	return nil
}

func (v *VMs) cleanupAll(ctx context.Context, vm *vmv1.VirtualMachine) {
	v.access.cleanup(ctx, vm.Name, v.useCloudNATGateways)
	if err := v.cloudClient.Delete(ctx, vm); err != nil {
		v.logger.Warn("createVM", "createVM failed. Failed to cleanup vm %s", vm.Name)
	}
}

func (v *VMs) waitForDiskReady(ctx context.Context, diskName string, timeout time.Duration) error {
	diskReadyChecker := func(obj client.Object) (bool, error) {
		disk, ok := obj.(*vmv1.VirtualMachineDisk)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachineDisk: %v", obj)
		}
		return disk.Status.Phase == vmv1.DiskPhaseSucceeded, nil
	}

	if err := v.waiter.Until(
		ctx,
		diskReadyChecker,
		diskName,
		&vmv1.VirtualMachineDisk{},
		timeout,
	); err != nil {
		return fmt.Errorf("wait until disk %q becomes ready: %w", diskName, err)
	}

	return nil
}

func (v *VMs) deleteDisk(ctx context.Context, diskName string) error {
	disk := &vmv1.VirtualMachineDisk{}
	if err := v.cloudClient.Get(ctx, client.ObjectKey{Name: diskName, Namespace: v.projectID}, disk); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("find disk %q: %v", diskName, err)
		}
		return nil
	}

	if err := v.cloudClient.Delete(ctx, disk); err != nil {
		return fmt.Errorf("delete disk %q: %v", diskName, err)
	}
	return nil
}

func (v *VMs) cleanupOrphans(ctx context.Context, vmName string) {
	bootDiskName := bootDiskName(vmName)
	if err := v.vmService.DeleteDisk(ctx, bootDiskName); err != nil {
		v.logger.Warn("createVM", "createVM failed. Failed to cleanup disk %s", bootDiskName)
	}

	ephemeralDiskName := ephemeralDiskName(vmName)
	if err := v.vmService.DeleteDisk(ctx, ephemeralDiskName); err != nil {
		v.logger.Warn("createVM", "createVM failed. Failed to cleanup disk %s", ephemeralDiskName)
	}
}

func bootDiskName(vmName string) string {
	return fmt.Sprintf("%s-boot-disk", vmName)
}

func ephemeralDiskName(vmName string) string {
	return fmt.Sprintf("%s-ephemeral-disk", vmName)
}

// Get network properties including VM static IP, gateway IP, and the associated NetworkCloudProps from 'manual' network
func getNetwork(networks apiv1.Networks) (string, string, network.NetworkCloudProperty, error) {
	// Only one network can be assigned to a VM
	for _, net := range networks {
		if net.Type() == "manual" && len(net.IP()) > 0 {
			var networkCloudProps network.NetworkCloudProperty
			if err := net.CloudProps().As(&networkCloudProps); err != nil {
				return "", "", network.NetworkCloudProperty{}, fmt.Errorf("parse network cloud properties: %w, %s", err, net.CloudProps())
			}

			return net.IP(), net.Gateway(), networkCloudProps, nil
		}
	}
	return "", "", network.NetworkCloudProperty{}, nil
}

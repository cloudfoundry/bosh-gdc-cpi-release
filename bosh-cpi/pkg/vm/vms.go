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
	"math"
	"strings"
	"time"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/agent"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	vmservice "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/vm"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/unit"

	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

var metadataRetryWait = wait.Waiting{
	Wait:  30 * time.Second,
	Every: 10 * time.Second,
}

type objectFinder interface {
	HasVM(ctx context.Context, vmID string) (bool, error)
	FindVM(ctx context.Context, vmID string) (*vmv1.VirtualMachine, error)
	FindVMIP(ctx context.Context, vmID string) (string, error)
	FindVMIngressIP(ctx context.Context, vmID string) (string, error)
	FindDisk(ctx context.Context, diskID string) (*vmv1.VirtualMachineDisk, error)
}

type imageService interface {
	Get(ctx context.Context, imageID string) (*vmv1.VirtualMachineImage, error)
}

type uuidGenerator interface {
	Generate() (uuid string, err error)
}

type waiter interface {
	Until(ctx context.Context, check wait.ConditionChecker, name string, obj client.Object, timeout time.Duration) error
	UntilDeleted(ctx context.Context, name string, obj client.Object, timeout time.Duration) error
	WaitForSuccess(ctx context.Context, msg string, timeout wait.Waiting, check func(context.Context) error) error
}

type metadataClient interface {
	Fetch(ctx context.Context, vmName string) (apiv1.AgentEnv, error)
	Update(ctx context.Context, vmName string, agentEnv apiv1.AgentEnv) error
}

type VMs struct {
	cloudClient         client.Client
	projectID           string
	uuidGen             uuidGenerator
	finder              objectFinder
	logger              cfboshlog.Logger
	agentOptions        apiv1.AgentOptions
	access              *Access
	agentEnvService     *agent.AgentEnvService
	imageService        imageService
	waiter              waiter
	vmService           *vmservice.VMService
	timeout             *config.WaitTimeout
	useMetadataServer   bool
	metadataClient      metadataClient
	useDiskHotplug      bool
	useCloudNATGateways bool
	useTier1Network     bool
	vmAccessService     vmservice.VMARAccessChecker
}

func NewVMs(
	cloudClient client.Client,
	projectID string,
	uuidGen uuidGenerator,
	agentOptions apiv1.AgentOptions,
	externalAccessPorts []config.ServerPort,
	logger cfboshlog.Logger,
	finder objectFinder,
	agentEnvService *agent.AgentEnvService,
	imaimageService imageService,
	waiter waiter,
	timeout *config.WaitTimeout,
	useMetadataServer bool,
	metadataClient metadataClient,
	useDiskHotplug bool,
	useCloudNATGateways bool,
	useTier1Network bool,
	vmAccessService vmservice.VMARAccessChecker,
) *VMs {
	access := NewAccess(cloudClient, projectID, logger, waiter, externalAccessPorts)
	return &VMs{
		cloudClient:     cloudClient,
		projectID:       projectID,
		uuidGen:         uuidGen,
		finder:          finder,
		logger:          logger,
		agentOptions:    agentOptions,
		access:          access,
		agentEnvService: agentEnvService,
		imageService:    imaimageService,
		waiter:          waiter,
		vmService: &vmservice.VMService{
			Client:    cloudClient,
			ProjectID: projectID,
			Waiter:    waiter,
			Logger:    logger,
			Timeout:   timeout,
		},
		timeout:             timeout,
		useMetadataServer:   useMetadataServer,
		metadataClient:      metadataClient,
		useDiskHotplug:      useDiskHotplug,
		useCloudNATGateways: useCloudNATGateways,
		useTier1Network:     useTier1Network,
		vmAccessService:     vmAccessService,
	}
}

// VM APIs.

func (v *VMs) CreateVM(
	agentID apiv1.AgentID, stemcellCID apiv1.StemcellCID,
	cloudProps apiv1.VMCloudProps, networks apiv1.Networks,
	associatedDiskCIDs []apiv1.DiskCID, env apiv1.VMEnv) (apiv1.VMCID, error) {
	vmID, _, err := v.CreateVMV2(agentID, stemcellCID, cloudProps, networks, associatedDiskCIDs, env)
	if err != nil {
		return apiv1.VMCID{}, err
	}
	return vmID, nil
}

func (v *VMs) CreateVMV2(
	agentID apiv1.AgentID, stemcellCID apiv1.StemcellCID,
	cloudProps apiv1.VMCloudProps, networks apiv1.Networks,
	associatedDiskCIDs []apiv1.DiskCID, env apiv1.VMEnv) (apiv1.VMCID, apiv1.Networks, error) {
	var prop Property
	if err := cloudProps.As(&prop); err != nil {
		return apiv1.VMCID{}, networks, fmt.Errorf("parse vm properties: %w", err)
	}

	ip, gatewayIP, networkCloudProps, err := getNetwork(networks)
	if err != nil {
		return apiv1.VMCID{}, networks, fmt.Errorf("get manual network: %w", err)
	}
	if len(gatewayIP) > 0 {
		v.logger.Info("createVM", "Network configured with gateway IP %q", gatewayIP)
	}

	if prop.Labels == nil {
		prop.Labels = make(map[string]string)
	}

	// Merge the `Labels` defined from BOSH network block in VM cloud properties.
	// `Labels` defined from BOSH network always take precedence in VM creation.
	if len(networkCloudProps.Labels) > 0 {
		v.logger.Info("createVM", "Merging labels from network cloud properties into VM cloud properties")

		for key, val := range networkCloudProps.Labels {
			if len(prop.Labels[key]) > 0 {
				v.logger.Warn("createVM", "Label %q from VM cloud properties overridden by network cloud properties", key)
			}
			prop.Labels[key] = val
		}
	}

	if len(networkCloudProps.SubnetName) > 0 {
		prop.SubnetName = networkCloudProps.SubnetName
	}

	if err := prop.validate(); err != nil {
		return apiv1.VMCID{}, networks, fmt.Errorf("validate vm properties: %w", err)
	}

	if len(prop.SubnetName) > 0 && len(ip) == 0 {
		return apiv1.VMCID{}, networks, errors.New("network.IP must be specified when vm prop SubnetName is provided")
	}

	ctx := context.Background()
	if err := v.validateDisks(ctx, associatedDiskCIDs); err != nil {
		return apiv1.VMCID{}, networks, fmt.Errorf("validate disks: %w", err)
	}

	genUUID, err := v.uuidGen.Generate()
	if err != nil {
		return apiv1.VMCID{}, networks, fmt.Errorf("generate vm uuid: %w", err)
	}

	// Make VM ID shorter to avoid length validation error
	vmUUID := strings.Split(genUUID, "-")[0]

	vmName := prop.Name
	if len(vmName) == 0 {
		vmName = fmt.Sprintf("vm-%s", vmUUID)
	}

	bootDiskName := bootDiskName(vmName)
	if err := v.createBootDisk(ctx, bootDiskName, vmName, stemcellCID, prop.BootDiskSizeGB, prop.BootDiskType); err != nil {
		return apiv1.VMCID{}, networks, fmt.Errorf("create boot disk: %w", err)
	}

	ephemeralDiskName := ephemeralDiskName(vmName)
	if err := v.createEphemeralDisk(ctx, ephemeralDiskName, vmName, prop.EphemeralDiskSizeGB, prop.EphemeralDiskType); err != nil {
		return apiv1.VMCID{}, networks, fmt.Errorf("create ephemeral disk: %w", err)
	}

	createVMInput := vmCreationInput{
		agentID:           agentID,
		vmName:            vmName,
		prop:              prop,
		bootDiskName:      bootDiskName,
		ephemeralDiskName: ephemeralDiskName,
		networks:          networks,
		env:               env,
		ip:                ip,
	}

	if err := v.createVM(ctx, createVMInput); err != nil {
		return apiv1.VMCID{}, networks, err
	}
	return apiv1.NewVMCID(vmName), networks, nil
}

func (v *VMs) DeleteVM(cid apiv1.VMCID) error {
	ctx := context.Background()
	vmName := cid.AsString()
	if err := v.vmService.Delete(ctx, vmName); err != nil {
		return fmt.Errorf("delete VM %q: %w", vmName, err)
	}
	return nil
}

// Returns most suitable machine type to satisfy the requested resource
// VMResources:
// - CPU (int)
// - RAM (MiB)
// - EphemeralDiskSize (MB)
func (v *VMs) CalculateVMCloudProperties(res apiv1.VMResources) (apiv1.VMCloudProps, error) {
	ctx := context.Background()
	vmTypes, err := v.vmService.ListVirtualMachineTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list VM types: %w", err)
	}

	// VM type with the nearest resource as requested
	var foundVMType *vmv1.VirtualMachineType

	resRAM := resource.NewQuantity(int64(res.RAM*unit.MiB), resource.BinarySI)
	for i, vmType := range vmTypes {
		vmTypeCPUS := int(vmType.Spec.VCPUs)
		vmTypeRAM := unit.ConvertToBytes(&vmType.Spec.Memory)

		// Skips the VM Type not satisfying the resource request
		if vmTypeCPUS < res.CPU || vmTypeRAM.Cmp(*resRAM) < 0 {
			continue
		}

		if foundVMType == nil {
			foundVMType = &vmTypes[i]
			continue
		}

		// Updates foundVMType if it has smaller resource
		foundVMTypeRAM := unit.ConvertToBytes(&foundVMType.Spec.Memory)
		if vmType.Spec.VCPUs <= foundVMType.Spec.VCPUs &&
			vmTypeRAM.Cmp(*foundVMTypeRAM) <= 0 {
			foundVMType = &vmTypes[i]
		}
	}

	if foundVMType == nil {
		return nil, fmt.Errorf("no virtual machine type found satisfying the requested resources: %+v", res)
	}

	return apiv1.NewVMCloudPropsFromMap(map[string]interface{}{
		"virtualMachineType": foundVMType.Name,
		// Get the nearest size in GB to satisfy the request
		"bootDiskSizeGB":      int(math.Ceil(float64(res.EphemeralDiskSize) / unit.KiB)),
		"ephemeralDiskSizeGB": int(math.Ceil(float64(res.EphemeralDiskSize) / unit.KiB)),
	}), nil
}

func (v *VMs) SetVMMetadata(cid apiv1.VMCID, metadata apiv1.VMMeta) error {
	ctx := context.Background()

	return v.waiter.WaitForSuccess(ctx, fmt.Sprintf("set VM %q metadata", cid.AsString()), metadataRetryWait, func(ctx context.Context) error {
		vm, err := v.finder.FindVM(ctx, cid.AsString())
		if err != nil {
			return fmt.Errorf("find vm %q: %w", cid.AsString(), err)
		}
		return v.setVMMetadata(ctx, vm, metadata)
	})
}

func (v *VMs) HasVM(cid apiv1.VMCID) (bool, error) {
	return v.finder.HasVM(context.Background(), cid.AsString())
}

func (v *VMs) RebootVM(cid apiv1.VMCID) error {
	ctx := context.Background()
	vm, err := v.finder.FindVM(ctx, cid.AsString())
	if err != nil {
		return fmt.Errorf("find vm %q: %w", cid.AsString(), err)
	}
	return v.PerformReboot(ctx, vm)
}

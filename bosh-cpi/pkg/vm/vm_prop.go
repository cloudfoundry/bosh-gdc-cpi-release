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
	"errors"
	"fmt"

	consts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/hashicorp/go-multierror"
)

// Defines VM GDC cloud properties for VM creation based on virtualmachine/v1 API.
// While boot disk image and the attached disks are provided separately at vm creation
type Property struct {
	Name string `json:"name,omitempty"`

	Labels map[string]string `json:"labels,omitempty"`

	// Specifies the name of the referenced `VirtualMachineType`, which takes precedence over custom spec.
	// The value can be queried by CalculateVMCloudProperties with desired CPU and memory config.
	// If the `virtualMachineType` is empty, VM instance will be provisioned based on the custom
	// spec defined from `vcpus` and `memorySizeGB`.
	VirtualMachineType string `json:"virtualMachineType,omitempty"`

	// Number of vCPUs requested on the VM instance. When `vcpus` is defined, `memorySizeGB` must be defined.
	VCPUs int `json:"vcpus,omitempty"`

	// Memory in GB requested on the VM instance. When `memorySizeGB` is defined, `vcpus` must be defined.
	MemorySizeGB int `json:"memorySizeGB,omitempty"`

	// Specifies the storage class type of the boot disk.
	// Currently if left empty defaults to Standard.
	// Can be set to Standard or Performance.
	BootDiskType string `json:"bootDiskType,omitempty"`

	// Specifies the storage class type of the ephemeral disk.
	// Currently if left empty defaults to Standard.
	// Can be set to Standard or Performance.
	EphemeralDiskType string `json:"ephemeralDiskType,omitempty"`

	// Specifies the size of the boot disk in GB. It is used together with stemcell to create boot disk.
	// If unset, default is inferred as being equivalent to `Image.ImageMetadata.minimumDiskSize`.
	// Otherwise, the specified size has to be greater than the `Image.ImageMetadata.minimumDiskSize`.
	BootDiskSizeGB int `json:"bootDiskSizeGB,omitempty"`

	// Specifies the size of the ephemeral disk in GB. The space in the ephemeral disk, with minimum size of 10GB, is used by BOSH agent running inside BOSH managed VM.
	EphemeralDiskSizeGB int `json:"ephemeralDiskSizeGB,omitempty"`

	// Specifies whether to enable the AccessManagement feature in the VM's guest environment.
	AccessEnabled bool `json:"accessEnabled,omitempty"`

	// Specifies the VM initialization options at boot time.
	Firmware VMFirmware `json:"firmware,omitempty"`

	// Specifies the load balancer for VM to associated with, when non-empty, all fields must be set.
	LoadBalancer LB `json:"lb,omitempty"`

	// Specifies the Cloud NAT gateway for VM to associated with, when non-empty, all fields must be set.
	CloudNAT CloudNAT `json:"cloudNAT,omitempty"`

	// Specifies if tier 1 network should be used for VM.
	UseTier1Network bool `json:"useTier1Network,omitempty"`

	// Specifies the custom subnet name for static IP assignment.
	// For Lancer GDCH, if `SubnetName` is provided, IP of the network parameter must be set.
	SubnetName string `json:"subnetName,omitempty"`

	// Zone in which vm should be created.
	Zone string `json:"zone"`

	// PublicKey is an optional SSH public key provided via cloud properties.
	// If set, CPI provisions a VMAR to make publish this key to the VM, independent of any other CPI-managed SSH keys.
	// The key must be in the format "<type> <blob> <comment>", e.g. "ssh-rsa AAAAB3NzaC1yc2E... <any string>".
	// Keys without a comment (2 parts) are not currently supported by GDCH.
	PublicKey string `json:"publicKey,omitempty"`
}

type VMFirmware struct {
	// Specifies whether to boot via UEFI or BIOS.
	// Defaults to `bios`.
	Type string `json:"type,omitempty"`

	// Enables or disables boot loader certificate verification.
	// This is to assist in blocking modified or malicious code from loading.
	// Defaults to `true`.
	EnableSecureBoot bool `json:"enableSecureBoot,omitempty"`
}

type LB struct {
	Name     string     `json:"name,omitempty"`
	Selector LBSelector `json:"selector,omitempty"`
}

type LBSelector struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

type CloudNAT struct {
	Name     string           `json:"name,omitempty"`
	Selector CloudNATSelector `json:"selector,omitempty"`
}

type CloudNATSelector struct {
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

func (p *Property) validate() error {
	var errs *multierror.Error

	if len(p.VirtualMachineType) == 0 && (p.VCPUs <= 0 || p.MemorySizeGB <= 0) {
		errs = multierror.Append(errs, errors.New("Must provide non-empty and positive VCPUs and MemorySizeGB when VirtualMachineType is empty"))
	}

	if len(p.BootDiskType) > 0 {
		if p.BootDiskType != consts.DiskTypeStandard && p.BootDiskType != consts.DiskTypePerformance {
			errs = multierror.Append(errs, fmt.Errorf("boot disk type must be one of either '%v' or '%v'", consts.DiskTypeStandard, consts.DiskTypePerformance))
		}
	} else if len(p.BootDiskType) == 0 {
		// If left boot disk type is empty, set the default to Standard disk type.
		p.BootDiskType = consts.DiskTypeStandard
	}
	if len(p.EphemeralDiskType) > 0 {
		if p.EphemeralDiskType != consts.DiskTypeStandard && p.EphemeralDiskType != consts.DiskTypePerformance {
			errs = multierror.Append(errs, fmt.Errorf("ephemeral disk type must be one of either '%v' or '%v'", consts.DiskTypeStandard, consts.DiskTypePerformance))
		}
	} else if len(p.EphemeralDiskType) == 0 {
		// If left ephemeral disk type is left empty, set the default to Standard disk type.
		p.EphemeralDiskType = consts.DiskTypeStandard
	}

	if p.BootDiskSizeGB <= 0 {
		errs = multierror.Append(errs, errors.New("Must provide positive BootDiskSizeGB"))
	}

	// minimum acceptable disk size is 10GB,
	// https://cloud.google.com/distributed-cloud/hosted/docs/latest/gdch/application/ao-user/vms/manage-storage/add-a-vm-disk
	if p.EphemeralDiskSizeGB < 10 {
		errs = multierror.Append(errs, errors.New("Minimum EphemeralDiskSizeGB is 10 GB"))
	}

	// If LoadBalancer is specified, all fields must be set
	lb := p.LoadBalancer
	if len(lb.Name) > 0 || len(lb.Selector.Key) > 0 || len(lb.Selector.Value) > 0 {
		if len(lb.Name) == 0 {
			errs = multierror.Append(errs, errors.New("Must provide non-empty Name of LoadBalancer"))
		}
		if len(lb.Selector.Key) == 0 {
			errs = multierror.Append(errs, errors.New("Must provide non-empty Key of LoadBalancer Selector"))
		}
		if len(lb.Selector.Value) == 0 {
			errs = multierror.Append(errs, errors.New("Must provide non-empty Value of LoadBalancer Selector"))
		}
	}

	// If CloudNAT is specified, all fields must be set
	cn := p.CloudNAT
	if len(cn.Name) > 0 || len(cn.Selector.Key) > 0 || len(cn.Selector.Value) > 0 {
		if len(cn.Name) == 0 {
			errs = multierror.Append(errs, errors.New("Must provide non-empty Name of CloudNAT"))
		}
		if len(cn.Selector.Key) == 0 {
			errs = multierror.Append(errs, errors.New("Must provide non-empty Key of CloudNAT Selector"))
		}
		if len(cn.Selector.Value) == 0 {
			errs = multierror.Append(errs, errors.New("Must provide non-empty Value of CloudNAT Selector"))
		}
	}

	return errs.ErrorOrNil()
}

func (p *Property) MultizoneValidate() error {
	var errs *multierror.Error

	if len(p.Zone) == 0 {
		errs = multierror.Append(errs, errors.New("Non-empty zone name must be provided"))
	}

	return errs.ErrorOrNil()
}

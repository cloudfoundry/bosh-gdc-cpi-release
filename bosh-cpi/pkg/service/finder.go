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

package service

import (
	"context"
	"fmt"
	"net"
	"sort"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

const vmNodeNetworkLancerInterfaceName = "net1"

type CPIFinder struct {
	client    client.Client
	projectID string
}

func NewCPIFinder(client client.Client, projectID string) *CPIFinder {
	return &CPIFinder{
		client:    client,
		projectID: projectID,
	}
}

func (c *CPIFinder) FindVM(ctx context.Context, vmID string) (*vmv1.VirtualMachine, error) {
	vm, err := c.getVM(ctx, vmID)
	if err != nil {
		return nil, fmt.Errorf("get VM (%s/%s): %w", c.projectID, vmID, err)
	}
	return vm, nil
}

func (c *CPIFinder) FindVMIngressIP(ctx context.Context, vmID string) (string, error) {
	vmea := &vmv1.VirtualMachineExternalAccess{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: vmID, Namespace: c.projectID}, vmea); err != nil {
		if errors.IsNotFound(err) {
			return "", err
		} else {
			return "", fmt.Errorf("unexpected error when fetching VM %s external access: %w", vmID, err)
		}
	}
	if vmea.Status.IngressIP == "" {
		return "", fmt.Errorf("VM %s external access ingress IP has not been populated yet", vmID)
	}

	return vmea.Status.IngressIP, nil
}

// Find VM IP which is only accessible from inside GDCH network
func (c *CPIFinder) FindVMIP(ctx context.Context, vmID string) (string, error) {
	vm, err := c.getVM(ctx, vmID)
	if err != nil {
		return "", err
	}

	var cidr string

	targetInterfaceName := vmNodeNetworkLancerInterfaceName
	for _, netif := range vm.Status.Network.Interfaces {
		if netif.Name == targetInterfaceName {
			if len(netif.IpAddresses) == 0 {
				return "", fmt.Errorf("VM %s node network interface IP has not been populated yet", vmID)
			}
			cidr = netif.IpAddresses[0]
			break
		}
	}
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse CIDR %q", cidr)
	}

	return ip.String(), nil
}

func (c *CPIFinder) FindDisk(ctx context.Context, diskID string) (*vmv1.VirtualMachineDisk, error) {
	disk, err := c.getDisk(ctx, diskID)
	if err != nil {
		return nil, fmt.Errorf("get disk (%s/%s): %w", c.projectID, diskID, err)
	}
	return disk, nil
}

func (c *CPIFinder) HasVM(ctx context.Context, vmID string) (bool, error) {
	if _, err := c.getVM(ctx, vmID); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		} else {
			return false, fmt.Errorf("get VM (%s/%s): %w", c.projectID, vmID, err)
		}
	}
	return true, nil
}

func (c *CPIFinder) HasDisk(ctx context.Context, diskID string) (bool, error) {
	if _, err := c.getDisk(ctx, diskID); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		} else {
			return false, fmt.Errorf("get disk (%s/%s): %w", c.projectID, diskID, err)
		}
	}
	return true, nil
}

func (c *CPIFinder) FindBackupPlans(ctx context.Context, diskID string) ([]vmv1.VirtualMachineBackupPlan, error) {
	vmbpList := &vmv1.VirtualMachineBackupPlanList{}
	if err := c.client.List(ctx, vmbpList, client.InNamespace(c.projectID), client.MatchingLabels{consts.VMBackupPlanLabelKey: diskID}); err != nil {
		return nil, fmt.Errorf("list backup plans for disk %s: %w", diskID, err)
	}

	sort.Slice(vmbpList.Items, func(i, j int) bool {
		return vmbpList.Items[i].CreationTimestamp.After(vmbpList.Items[j].CreationTimestamp.Time)
	})

	return vmbpList.Items, nil
}

func (c *CPIFinder) getVM(ctx context.Context, vmID string) (*vmv1.VirtualMachine, error) {
	vm := &vmv1.VirtualMachine{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: vmID, Namespace: c.projectID}, vm); err != nil {
		return nil, err
	}

	return vm, nil
}

func (c *CPIFinder) getDisk(ctx context.Context, diskID string) (*vmv1.VirtualMachineDisk, error) {
	disk := &vmv1.VirtualMachineDisk{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: diskID, Namespace: c.projectID}, disk); err != nil {
		return nil, err
	}
	return disk, nil
}

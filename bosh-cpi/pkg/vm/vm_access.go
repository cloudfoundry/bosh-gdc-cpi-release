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

	consts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"

	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

var timeoutVMAccessReady = 3 * time.Minute

// Manages VM access outside the project
type Access struct {
	cloudClient         client.Client
	logger              cfboshlog.Logger
	projectID           string
	waiter              waiter
	externalAccessPorts []config.ServerPort
}

func NewAccess(
	cloudClient client.Client,
	projectID string,
	logger cfboshlog.Logger,
	waiter waiter,
	externalAccessPorts []config.ServerPort,
) *Access {
	return &Access{
		cloudClient:         cloudClient,
		logger:              logger,
		projectID:           projectID,
		waiter:              waiter,
		externalAccessPorts: externalAccessPorts,
	}
}

func (a *Access) create(ctx context.Context, vmName string, useCloudNATGateways bool) error {
	// Creates VM  access CRs
	vmea := a.virtualMachineExternalAccess(vmName, useCloudNATGateways)
	if err := a.cloudClient.Create(ctx, vmea); err != nil {
		return fmt.Errorf("create VirtualMachineExternalAccess for VM %s: %w", vmName, err)
	}

	if err := a.waitForVMEAReady(ctx, vmea.Name); err != nil {
		return fmt.Errorf(
			"wait for VirtualMachineExternalAccess %q to be ready: %w",
			vmea.Name, err)
	}

	return nil
}

func (a *Access) cleanup(ctx context.Context, vmName string, useCloudNATGateways bool) {
	vmea := a.virtualMachineExternalAccess(vmName, useCloudNATGateways)
	if err := a.cloudClient.Delete(ctx, vmea); err != nil {
		a.logger.Warn("vmCleanup", "failed to delete VirtualMachineExternalAccess for VM %s: %v", vmName, err)
	}
}

func (a *Access) virtualMachineExternalAccess(vmName string, useCloudNATGateways bool) *vmv1.VirtualMachineExternalAccess {
	ports := make([]vmv1.ServicePort, len(a.externalAccessPorts))
	for i, port := range a.externalAccessPorts {
		ports[i] = vmv1.ServicePort{
			Name:     port.Name,
			Protocol: corev1.Protocol(port.Protocol),
			Port:     port.Port,
		}
	}

	var annotations map[string]string
	if useCloudNATGateways {
		a.logger.Info("Cloud NAT Egress gateways are enabled. Applying Cloud NAT Enabled annotation: \"%v\":true to VMEA", consts.UseCloudNATEgressControlAnnotationKey)
		annotations = map[string]string{}
		annotations[consts.UseCloudNATEgressControlAnnotationKey] = "true"
	}
	return &vmv1.VirtualMachineExternalAccess{
		ObjectMeta: metav1.ObjectMeta{
			Name:        vmName,
			Namespace:   a.projectID,
			Annotations: annotations,
		},
		Spec: vmv1.VirtualMachineExternalAccessSpec{
			Enabled: true,
			Ports:   ports,
		},
	}
}

// Checks the ingressIP in external access status is populated.
func (a *Access) waitForVMEAReady(ctx context.Context, name string) error {
	ipChecker := func(obj client.Object) (bool, error) {
		vmea, ok := obj.(*vmv1.VirtualMachineExternalAccess)
		if !ok {
			return false, fmt.Errorf("convert object to VirtualMachineExternalAccess: %v", obj)
		}
		return vmea.Status.IngressIP != "", nil
	}

	if err := a.waiter.Until(
		ctx,
		ipChecker,
		name,
		&vmv1.VirtualMachineExternalAccess{},
		timeoutVMAccessReady,
	); err != nil {
		return fmt.Errorf("wait for IngressIP: %w", err)
	}
	return nil
}

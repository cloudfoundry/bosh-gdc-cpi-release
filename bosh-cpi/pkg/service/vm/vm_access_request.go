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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

var (
	vmarReadyWait = wait.Waiting{
		Wait:  10 * time.Minute,
		Every: 2 * time.Second,
	}
)

type VMARAccessChecker interface {
	EnsureVMARExistAndReady(vmName string, sshPublicKey string) error
}

type vmarWaiter interface {
	Until(ctx context.Context, check wait.ConditionChecker, name string, obj client.Object, timeout time.Duration) error
}

type VMAccessRequestService struct {
	cloudClient       client.Client
	projectID         string
	sshUser           string
	vmAccessReadyWait wait.Waiting
	waiter            vmarWaiter
}

func NewVMAccessRequestService(
	cloudClient client.Client,
	projectID string,
	sshUser string,
	waiter vmarWaiter,
) *VMAccessRequestService {
	return &VMAccessRequestService{
		cloudClient:       cloudClient,
		sshUser:           sshUser,
		projectID:         projectID,
		vmAccessReadyWait: vmarReadyWait,
		waiter:            waiter,
	}
}

// EnsureVMARExistAndReady ensures the VMAR(VirtualMachineAccessRequest) associated with the VM exists (creates if not) and ready for VM able to access by ssh
func (ar *VMAccessRequestService) EnsureVMARExistAndReady(vmName string, sshPublicKey string) error {
	ctx := context.Background()
	vmar, err := ar.getOrCreate(ctx, vmName, sshPublicKey)

	if err != nil {
		return fmt.Errorf("got VM %s access request: %w ", vmName, err)
	}

	if err := ar.waitForVMARReady(ctx, vmar); err != nil {
		return fmt.Errorf("wait for vm access request %s ready: %w", vmar.Name, err)
	}
	return nil
}

func (ar *VMAccessRequestService) getOrCreate(ctx context.Context, vmName string, sshPublicKey string) (*vmv1.VirtualMachineAccessRequest, error) {
	vmarList := &vmv1.VirtualMachineAccessRequestList{}
	if err := ar.cloudClient.List(ctx, vmarList, &client.ListOptions{
		Namespace: ar.projectID}); err != nil {
		return nil, fmt.Errorf("list access request for VM %s: %w", vmName, err)
	}

	var got *vmv1.VirtualMachineAccessRequest
	now := time.Now()
	for i, vmar := range vmarList.Items {
		if vmar.Spec.VM == vmName &&
			vmar.Spec.SSH.Key == sshPublicKey &&
			vmar.Status.State != vmv1.VMAccessRequestStateFailed &&
			// TODO(b/330531551) Filter expired VMAR with the new VMAR state
			!now.After(vmar.CreationTimestamp.Time.Add(vmar.Spec.SSH.TTL.Duration)) {
			got = &vmarList.Items[i]
		}
		if got != nil && got.Status.State == vmv1.VMAccessRequestStateConfigured {
			break
		}
	}

	if got == nil {
		got = ar.virtualMachineAccessRequest(vmName, sshPublicKey)
		if err := ar.cloudClient.Create(ctx, got); err != nil {
			return nil, fmt.Errorf("create access request for VM %s: %w", vmName, err)
		}
	}

	return got, nil
}

// Checks VM access request is configured
func (ar *VMAccessRequestService) waitForVMARReady(ctx context.Context, vmar *vmv1.VirtualMachineAccessRequest) error {
	checkIPPopulated := func(obj client.Object) (bool, error) {
		vmar, ok := obj.(*vmv1.VirtualMachineAccessRequest)
		if !ok {
			return false, fmt.Errorf("convert obj to VirtualMachineAccessRequest: %v", obj)
		}
		return vmar.Status.State == vmv1.VMAccessRequestStateConfigured, nil
	}

	if ar.vmAccessReadyWait.Wait > 0 {
		if err := ar.waiter.Until(
			ctx,
			checkIPPopulated,
			vmar.Name,
			&vmv1.VirtualMachineAccessRequest{},
			ar.vmAccessReadyWait.Wait,
		); err != nil {
			return fmt.Errorf("wait vm access request %s as configured after %v", vmar.Name, ar.vmAccessReadyWait.Wait)
		}
	}

	return nil
}

func (ar *VMAccessRequestService) virtualMachineAccessRequest(vmName string, publicKey string) *vmv1.VirtualMachineAccessRequest {
	return &vmv1.VirtualMachineAccessRequest{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", vmName),
			Namespace:    ar.projectID,
		},
		Spec: vmv1.VirtualMachineAccessRequestSpec{
			SSH: vmv1.SSHSpec{
				Key: publicKey,
				TTL: metav1.Duration{
					Duration: 24 * time.Hour,
				},
			},
			User: ar.sshUser,
			VM:   vmName,
		},
	}
}

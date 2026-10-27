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

	"sigs.k8s.io/controller-runtime/pkg/client"

	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

type VMChecker struct {
	CloudClient client.Client
	ProjectID   string
}

func (vmc *VMChecker) EnsureVMExistAndRunning(vmName string) error {
	ctx := context.Background()
	runningState := vmv1.VirtualMachineStateRunning

	vm := &vmv1.VirtualMachine{}
	if err := vmc.CloudClient.Get(ctx, client.ObjectKey{Name: vmName, Namespace: vmc.ProjectID}, vm); err != nil {
		return fmt.Errorf("unable to get VM: %w", err)
	}

	if vm.Status.State != runningState {
		return fmt.Errorf("Got VM %s state %s, expected is %v", vmName, vm.Status.State, runningState)
	}

	return nil
}

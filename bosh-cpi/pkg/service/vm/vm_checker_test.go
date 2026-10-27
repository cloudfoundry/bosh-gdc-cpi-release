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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

func TestEnsureVMExistAndRunningSuccess(t *testing.T) {
	vm := virtualMachineWithState(vmv1.VirtualMachineStateRunning)
	c := test.FakeClient(vm)
	vmChecker := &VMChecker{
		CloudClient: c,
		ProjectID:   projectID,
	}

	if err := vmChecker.EnsureVMExistAndRunning(vmName); err != nil {
		t.Fatalf("got error: %v", err)
	}
}

func TestEnsureVMExistAndRunningFailed(t *testing.T) {
	vm := virtualMachineWithState(vmv1.VirtualMachineStateStopped)
	c := test.FakeClient(vm)
	vmChecker := &VMChecker{
		CloudClient: c,
		ProjectID:   projectID,
	}

	if err := vmChecker.EnsureVMExistAndRunning(vmName); err == nil {
		t.Fatal("not got error, expected error")
	}
}

func virtualMachineWithState(state vmv1.VirtualMachineState) *vmv1.VirtualMachine {
	return &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: projectID,
		},
		Status: vmv1.VirtualMachineStatus{
			State: state,
		},
	}
}

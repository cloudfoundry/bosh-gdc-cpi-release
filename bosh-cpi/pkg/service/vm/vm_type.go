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

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

// ListVirtualMachineTypes retrieves a list of `VirtualMachineType` CRs from the
// namespace defined by `consts.VMSystemNamespace`.
func (vm *VMService) ListVirtualMachineTypes(ctx context.Context) ([]vmv1.VirtualMachineType, error) {
	vmtl := &vmv1.VirtualMachineTypeList{}
	if err := vm.Client.List(ctx, vmtl, client.InNamespace(consts.VMSystemNamespace)); err != nil {
		return nil, fmt.Errorf("list `VirtualMachineType` in namespace %q: %w",
			consts.VMSystemNamespace, err)
	}
	return vmtl.Items, nil
}

// GetVirtualMachineType retrieves a specific `VirtualMachineType` CR by name from the
// namespace defined by `consts.VMSystemNamespace`.
func (vm *VMService) GetVirtualMachineType(ctx context.Context, name string) (*vmv1.VirtualMachineType, error) {
	vmt := &vmv1.VirtualMachineType{}
	if err := vm.Client.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: consts.VMSystemNamespace,
	}, vmt); err != nil {
		return nil, fmt.Errorf("get `VirtualMachineType` %q in namespace %q: %w",
			name, consts.VMSystemNamespace, err)
	}
	return vmt, nil
}

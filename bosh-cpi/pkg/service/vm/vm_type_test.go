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

package vm_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/vm"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

func TestListVirtualMachineTypes(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := vmv1.AddToScheme(scheme); err != nil {
		t.Fatalf("vmv1.AddToScheme(scheme) = %v", err)
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			vmType("t1", "vm-system"),
			vmType("t2", "vm-system"),
			vmType("t3", "ns-foo"),
			vmType("t4", "ns-bar"),
		).
		Build()
	vm := &vm.VMService{Client: c}
	expectedTypes := []vmv1.VirtualMachineType{
		*vmType("t1", "vm-system"),
		*vmType("t2", "vm-system"),
	}

	vmtl, err := vm.ListVirtualMachineTypes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if diff := cmp.Diff(expectedTypes, vmtl,
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); diff != "" {
		t.Errorf("unexpected VM types (-want +got):\n%s", diff)
	}
}

func TestGetVirtualMachineType(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := vmv1.AddToScheme(scheme); err != nil {
		t.Fatalf("vmv1.AddToScheme(scheme) = %v", err)
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			vmType("t1", "vm-system"),
			vmType("t2", "vm-system"),
		).
		Build()
	vmSvc := &vm.VMService{Client: c}

	testCases := []struct {
		name        string
		machineType string
		expectError bool
		expected    *vmv1.VirtualMachineType
	}{
		{
			name:        "found",
			machineType: "t1",
			expectError: false,
			expected:    vmType("t1", "vm-system"),
		},
		{
			name:        "not found",
			machineType: "non-existent",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := vmSvc.GetVirtualMachineType(context.Background(), tc.machineType)

			if tc.expectError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.expected, got,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); diff != "" {
				t.Errorf("unexpected VM type (-want +got):\n%s", diff)
			}
		})
	}
}

func vmType(name, namespace string) *vmv1.VirtualMachineType {
	return &vmv1.VirtualMachineType{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
}

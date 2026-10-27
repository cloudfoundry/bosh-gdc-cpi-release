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

import "testing"

func TestIsTier1NetworkSupported(t *testing.T) {
	testCases := []struct {
		name        string
		machineType string
		vcpus       uint32
		expected    bool
	}{
		{name: "N2 with 8 vCPUs is supported", machineType: "n2-standard-8", vcpus: 8, expected: true},
		{name: "N2 with 7 vCPUs is not supported", machineType: "n2-standard-7", vcpus: 7, expected: false},
		{name: "N2 with 16 vCPUs is supported", machineType: "n2-standard-16", vcpus: 16, expected: true},
		{name: "N3 with 8 vCPUs is supported", machineType: "n3-standard-8", vcpus: 8, expected: true},
		{name: "N3 with 7 vCPUs is not supported", machineType: "n3-standard-7", vcpus: 7, expected: false},
		{name: "N4 with 16 vCPUs is supported", machineType: "n4-highcpu-16", vcpus: 16, expected: true},
		{name: "N4 with 15 vCPUs is not supported", machineType: "n4-highcpu-15", vcpus: 15, expected: false},
		{name: "N4 with 32 vCPUs is supported", machineType: "n4-highcpu-32", vcpus: 32, expected: true},
		{name: "A2 with 12 vCPUs is supported", machineType: "a2-highgpu-1g", vcpus: 12, expected: true},
		{name: "A2 with 11 vCPUs is not supported", machineType: "a2-highgpu-1g", vcpus: 11, expected: false},
		{name: "A3 with 28 vCPUs is supported", machineType: "a3-highgpu-8g", vcpus: 28, expected: true},
		{name: "A3 with 27 vCPUs is not supported", machineType: "a3-highgpu-8g", vcpus: 27, expected: false},
		{name: "Custom VM is not supported", machineType: "custom-1-1024", vcpus: 32, expected: false},
		{name: "Unsupported family is not supported", machineType: "e2-standard-8", vcpus: 8, expected: false},
		{name: "Empty machine type is not supported", machineType: "", vcpus: 8, expected: false},
		{name: "Machine type with no vCPUs is not supported", machineType: "n2-standard-8", vcpus: 0, expected: false},
		{name: "N2 mixed case supported", machineType: "N2-StAndArd-8", vcpus: 8, expected: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTier1NetworkSupported(tc.machineType, tc.vcpus); got != tc.expected {
				t.Errorf("isTier1Supported(%q, %d) = %v, want %v", tc.machineType, tc.vcpus, got, tc.expected)
			}
		})
	}
}

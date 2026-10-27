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

import "strings"

// Determines if a given machine type and vCPU count are eligible for Tier 1 networking.
// TODO(b/473616830): This logic is obtained from public documentation (source of record) and should be removed in favor of golang package VMM team will create to encapsulate this logic:
// See: https://docs.cloud.google.com/distributed-cloud/hosted/docs/next/gdch/application/ao-user/vms/manage-vms/operations-and-lifecycle/vm-config-network#limitations
func IsTier1NetworkSupported(machineType string, vCPUs uint32) bool {
	thresholds := map[string]int{
		"n2": 8, "n3": 8,
		"n4": 16,
		"a2": 12,
		"a3": 28,
	}

	machineType = strings.ToLower(machineType)
	vmFamily := strings.Split(machineType, "-")[0]

	if threshold, ok := thresholds[vmFamily]; ok {
		return vCPUs >= uint32(threshold)
	}

	return false
}

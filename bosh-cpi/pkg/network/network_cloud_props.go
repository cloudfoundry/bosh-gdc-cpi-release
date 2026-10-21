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

package network

// Defines GDCH network cloud properties defined from BOSH network blocks
type NetworkCloudProperty struct {
	// Labels defined from network block for 'manual' network will be merged with the labels from BOSH `resource_pools` as part of
	// VM cloud properties, which will be propogated as the labels of VM instance.
	Labels map[string]string `json:"labels,omitempty"`

	// The name of the Lancer GDCH Subnet from which the IP is allocated. SubnetName defined in the BOSH network block's cloud properties.
	SubnetName string `json:"subnetName,omitempty"`
}

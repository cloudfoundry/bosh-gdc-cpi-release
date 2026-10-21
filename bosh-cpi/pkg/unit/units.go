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

package unit

import (
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Define common units
const (
	KiB           = 1024
	MiB           = 1024 * KiB
	GiB           = 1024 * MiB
	KBToKiBFactor = 1.024
	MBToMiBFactor = KBToKiBFactor * KBToKiBFactor
	GBToGiBFactor = MBToMiBFactor * KBToKiBFactor
)

// Converts units. E.g., convert resource from GB to GiB. This is a workaround for b/305825868
// TODO(b/305825868): Memory in pre-defined types are in GB, which will be updated to GiB.
func ConvertToBytes(quantity *resource.Quantity) *resource.Quantity {
	if quantity.Format == resource.BinarySI {
		return quantity
	}

	value := float64(quantity.Value())
	// Round to 1 KiB for value smaller than 1 KB
	if value < math.Pow10(3) {
		return resource.NewQuantity(KiB, resource.BinarySI)
	}

	if value < math.Pow10(6) {
		return resource.NewQuantity(int64(value*KBToKiBFactor), resource.BinarySI)
	}
	if value < math.Pow10(9) {
		return resource.NewQuantity(int64(value*MBToMiBFactor), resource.BinarySI)
	}
	if value < math.Pow10(12) {
		return resource.NewQuantity(int64(value*GBToGiBFactor), resource.BinarySI)
	}

	// Round to 1 TiB for value larger than 1 TB
	return resource.NewQuantity(GiB*KiB, resource.BinarySI)
}

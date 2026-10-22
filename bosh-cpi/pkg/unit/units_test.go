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
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func TestConvertGBToGiB(t *testing.T) {
	kiBQuantity := resource.NewQuantity(KiB, resource.BinarySI)
	miBQuantity := resource.NewQuantity(MiB, resource.BinarySI)
	giBQuantity := resource.NewQuantity(GiB, resource.BinarySI)
	tiBQuantity := resource.NewQuantity(GiB*KiB, resource.BinarySI)

	testCases := map[string]struct {
		input  *resource.Quantity
		expect *resource.Quantity
	}{
		"Round to 1 KiB for value below 1 KB": {
			input:  resource.NewQuantity(999, resource.DecimalExponent),
			expect: kiBQuantity,
		},
		"Convert KB to KiB": {
			input:  resource.NewQuantity(int64(math.Pow10(3)), resource.DecimalExponent),
			expect: kiBQuantity,
		},
		"Convert MB to MiB": {
			input:  resource.NewQuantity(int64(math.Pow10(6)), resource.DecimalExponent),
			expect: miBQuantity,
		},
		"Convert GB to GiB": {
			input:  resource.NewQuantity(int64(math.Pow10(9)), resource.DecimalExponent),
			expect: giBQuantity,
		},
		"Round to 1 TiB for value above 1 TB": {
			input:  resource.NewQuantity(int64(math.Pow10(12)+1), resource.DecimalExponent),
			expect: tiBQuantity,
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			q := ConvertToBytes(tc.input)

			if q.Cmp(*tc.expect) != 0 {
				t.Fatalf("Test failed, got %v, expected %v", q, tc.expect)
			}
		})
	}
}

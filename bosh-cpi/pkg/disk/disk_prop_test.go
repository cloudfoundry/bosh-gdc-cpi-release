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

package disk

import (
	"strings"
	"testing"

	consts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
)

func TestValidate(t *testing.T) {
	testCases := map[string]struct {
		property Property
		wantErr  bool
	}{
		"Empty disk type defaults to Standard disk type": {
			property: Property{
				Zone: "zone-1",
			},
			wantErr: false,
		},
		"Standard disk type": {
			property: Property{
				DiskType: consts.DiskTypeStandard,
				Zone:     "zone-1",
			},
			wantErr: false,
		},
		"Performance disk type": {
			property: Property{
				DiskType: consts.DiskTypePerformance,
			},
			wantErr: false,
		},
		"Empty property defaults to Standard disk type": {
			property: Property{},
			wantErr:  false,
		},
		"Unsupported disk type": {
			property: Property{
				DiskType: "Unsupported",
				Zone:     "zone-1",
			},
			wantErr: true,
		},
		"Disk type with incorrect casing": {
			property: Property{
				DiskType: strings.ToLower(consts.DiskTypeStandard),
				Zone:     "zone-1",
			},
			wantErr: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			emptyDiskType := len(tc.property.DiskType) == 0
			err := tc.property.validate()
			if tc.wantErr && err == nil {
				t.Fatalf("%v: tc.property.validate()=%v, want: error", id, err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("%v: tc.property.validate()=%v, want: no error", id, err)
			}
			if emptyDiskType && tc.property.DiskType != consts.DiskTypeStandard {
				t.Fatalf("%v: tc.property.validate()='%s', want: '%s'", id, tc.property.DiskType, consts.DiskTypeStandard)
			}
		})
	}
}

func TestMultizoneValidate(t *testing.T) {
	testCases := map[string]struct {
		property Property
		wantErr  bool
	}{
		"Zone is not empty": {
			property: Property{
				Zone: "zone-1",
			},
			wantErr: false,
		},
		"Zone is empty (invalid)": {
			property: Property{
				Zone: "",
			},
			wantErr: true,
		},
		"Empty property (invalid zone)": {
			property: Property{},
			wantErr:  true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			err := tc.property.multizoneValidate()
			if tc.wantErr && err == nil {
				t.Fatalf("%v: multizoneValidate()=%v, want: error", id, err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("%v: multizoneValidate()=%v, want: no error", id, err)
			}
		})
	}
}

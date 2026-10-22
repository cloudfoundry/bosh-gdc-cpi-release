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

package misc

import (
	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
)

type Misc struct{}

func NewMisc() *Misc {
	return &Misc{}
}

// Returns CPI information
func (m *Misc) Info() (apiv1.Info, error) {
	return apiv1.Info{
		// Referenced by GDCH stemcell builder  "BOSH::Stemcell::GDC::Infrastructure"
		StemcellFormats: []string{"gdch-rawdisk"},
		// By docs-bosh, new CPIs adopting the V2 contract must return 2
		APIVersion: 2,
	}, nil
}

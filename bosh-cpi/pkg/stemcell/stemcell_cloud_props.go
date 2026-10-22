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

// Implement bosh-cpi-go/apiv1.StemcellCloudProps interface
package stemcell

import "encoding/json"

type StemcellCloudPropsImpl struct {
	json.RawMessage
}

var _ json.Marshaler = &StemcellCloudPropsImpl{}

func (c *StemcellCloudPropsImpl) As(val interface{}) error {
	return json.Unmarshal([]byte(c.RawMessage), val)
}

func (c *StemcellCloudPropsImpl) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.RawMessage)
}

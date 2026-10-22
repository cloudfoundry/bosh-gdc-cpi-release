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

package test

import (
	"encoding/json"
	"fmt"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
)

func ConvertAgentEnvToJsonMap(env apiv1.AgentEnv) (map[string]interface{}, error) {
	bytes, err := env.AsBytes()
	if err != nil {
		return nil, fmt.Errorf("convert env to bytes: %w", err)
	}

	var jsonMap map[string]interface{}
	err = json.Unmarshal(bytes, &jsonMap)
	if err != nil {
		return nil, fmt.Errorf("convert env to json: %w", err)
	}
	return jsonMap, nil
}

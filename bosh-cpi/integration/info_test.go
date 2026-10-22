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

package integration

import (
	"encoding/json"
	"testing"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"github.com/google/go-cmp/cmp"
)

const (
	infoMethod = "info"
)

func TestInfo(t *testing.T) {
	request := getRequest(infoMethod, nil)
	jsonData, err := executor.exec(request)
	if err != nil {
		t.Fatal(err)
	}

	var got apiv1.Info
	if err := json.Unmarshal(jsonData, &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	expected := apiv1.Info{
		StemcellFormats: []string{"gdch-rawdisk"},
		APIVersion:      2,
	}
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("test requests diff (-want +got):\n%s", diff)
	}
}

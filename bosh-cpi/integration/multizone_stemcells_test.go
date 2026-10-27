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
	"testing"
)

func TestMultizoneStemcellMethods(t *testing.T) {
	if gdcConfig.StemcellInfo.ImagePath == "" {
		logger.Info(logTag, "stemcell image file path not provided, skipping multizone stemcell integration test")
		return
	}
	if gdcConfig.StemcellInfo.ManifestPath == "" {
		logger.Info(logTag, "stemcell manifest file path not provided, skipping multizone stemcell integration test")
		return
	}
	if gdcConfig.OrgAPIEndpoints.ZonalAPIs == nil || len(gdcConfig.OrgAPIEndpoints.ZonalAPIs) == 0 {
		t.Fatalf("zonal APIs not provided")
	}

	var stemcellCID string
	t.Run("CreateMultizoneStemcell", func(t *testing.T) {
		stemcellCID = testCreateStemcell(cloudClient, gdcConfig.StemcellInfo.ImagePath, gdcConfig.StemcellInfo.ManifestPath, t)
	})

	// Always clean up at the end of tests suite
	if len(stemcellCID) > 0 {
		t.Cleanup(func() {
			t.Log("Running cleanup: DeleteMultizoneStemcell")
			testDeleteStemcell(cloudClient, stemcellCID, t)
		})
	}
}

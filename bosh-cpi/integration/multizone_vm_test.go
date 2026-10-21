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

func TestMultizoneVMAndDiskMethods(t *testing.T) {
	for _, zone := range zones {
		kubeClient := kubeClients.ZonalAPIServers[zone]
		if err := createProjectNetworkPolicy(kubeClient); err != nil {
			t.Fatal(err)
		}
		imageName := getImageName(kubeClient, t)

		mzConfig := &mzTestConfig{
			zone: zone,
		}
		var vmID string
		t.Run("CreateVM", func(t *testing.T) {
			vmID = testCreateVM(imageName, t, *mzConfig)
		})
		mzConfig.vmId = vmID

		testCases := map[string]func(string, *testing.T, ...mzTestConfig){
			"HasVM":         testHasVM,
			"SetVMMetadata": testSetVMMetadata,
			"RebootVM":      testRebootVM,
			"GetDisks":      testGetDisks,
		}

		for tn, st := range testCases {
			t.Run(tn, func(t *testing.T) {
				st(vmID, t, *mzConfig)
			})
		}

		testDiskWithVMMethods(vmID, t, *mzConfig)

		// Always clean up at the end of tests suite
		if len(vmID) > 0 {
			t.Cleanup(func() {
				t.Log("Running cleanup: DeleteVM")
				testDeleteVM(vmID, t, *mzConfig)
			})
		}

		if useStemcell && len(imageName) > 0 {
			t.Cleanup(func() {
				t.Log("Running cleanup: DeleteStemcell")
				testDeleteStemcell(kubeClient, imageName, t)
			})
		}
	}
}

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

func TestMultizoneDiskMethods(t *testing.T) {
	for _, zone := range zones {
		mzConfig := &mzTestConfig{
			zone: zone,
		}
		kubeClient := getKubeClient(*mzConfig)
		var vmID string
		imageName := getImageName(kubeClient, t)
		t.Run("CreateVM", func(t *testing.T) {
			vmID = testCreateVM(imageName, t, *mzConfig)
		})
		mzConfig.vmId = vmID
		var diskID string
		t.Run("CreateDisk", func(t *testing.T) {
			diskID = testCreateDisk(t, *mzConfig)
		})
		testCases := map[string]func(string, *testing.T, ...mzTestConfig){
			"HasDisk":         testHasDisk,
			"SetDiskMetadata": testSetDiskMetadata,
			"ResizeDisk":      testResizeDisk,
		}

		for tn, st := range testCases {
			t.Run(tn, func(t *testing.T) {
				st(diskID, t, *mzConfig)
			})
		}

		t.Run("TestResizeDiskHotExpansion", func(t *testing.T) {
			testResizeDiskHotExpansion(t, *mzConfig)
		})

		var backupID string
		t.Run("SnapshotDisk", func(t *testing.T) {
			backupID = testSnapshotDisk(diskID, t, *mzConfig)
		})

		// Always clean up at the end of tests suite
		if len(backupID) > 0 {
			t.Cleanup(func() {
				t.Log("Running cleanup: DeleteSnapshot")
				testDeleteSnapshot(backupID, t, *mzConfig)
			})
		}

		if len(diskID) > 0 {
			t.Cleanup(func() {
				t.Log("Running cleanup: DeleteDisk")
				testDeleteDisk(diskID, t, *mzConfig)
			})
		}

		if len(vmID) > 0 {
			t.Cleanup(func() {
				t.Log("Running cleanup: DeleteVM")
				testDeleteVM(vmID, t, *mzConfig)
			})
		}
	}
}

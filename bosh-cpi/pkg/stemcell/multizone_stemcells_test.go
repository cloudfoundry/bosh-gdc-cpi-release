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

package stemcell

import (
	"errors"
	"fmt"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeclients "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	vmconsts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
)

var (
	multiZoneUUIDs = []string{
		"aa937100-7c2f-11ee-b962-0242ac120002",
		"bb827500-1a9z-22ee-b962-0242ac121234",
	}
)

func TestMultizoneStemcells(t *testing.T) {
	gdcConfig := config.GDCConfig{
		OrgAdminURL: "https://zone1.com",
		OrgAPIEndpoints: config.OrgAPIConfig{
			ZonalAPIs: map[string]string{
				"zone1": "https://zone1.com",
				"zone2": "https://zone2.com",
			},
		},
	}
	config := config.Config{
		GDC: gdcConfig,
	}
	clients := kubeclients.KubeClients{
		ZonalAPIServers: make(map[string]client.WithWatch),
	}
	multiZoneStemcells, _ := NewMultizoneStemcells(&clients, nil, nil, &config)
	if multiZoneStemcells.leaderZone != "zone1" {
		t.Fatalf("got leader zone %s, want zone1", multiZoneStemcells.leaderZone)
	}
}

func TestCreateMultizoneStemcells(t *testing.T) {
	testCases := map[string]struct {
		leaderZone     string
		zonalStemcells map[string]*Stemcells
		wantError      error
	}{
		"error creating stemcell image in leader zone": {
			leaderZone: "zone1",
			zonalStemcells: map[string]*Stemcells{
				"zone1": NewStemcells(
					test.FakeClient(),
					projectID,
					defaultStorage,
					&mockImageServiceErrorCheckImage{},
					&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[0]},
					logger, &test.Timeout,
					false,
				),
				"zone2": NewStemcells(
					test.FakeClient(),
					projectID,
					defaultStorage,
					&mockImageServiceErrorCheckImage{},
					&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[1]},
					logger, &test.Timeout,
					false,
				),
			},
			wantError: errors.New(
				"Create stemcell in leader zone \"zone1\": " +
					"import stemcell image: import stemcell image as stemcell-0.0.0-aa937100: cannot import image"),
		},
		"error waiting for image replication": {
			leaderZone: "zone1",
			zonalStemcells: map[string]*Stemcells{
				"zone1": NewStemcells(
					test.FakeClient(),
					projectID,
					defaultStorage,
					&mockImageService{existingImage: createVirtualMachineImageWithLabel(defaultStemcellID.AsString())},
					&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[0]},
					logger, &test.Timeout,
					false,
				),
				"zone2": NewStemcells(
					test.FakeClient(),
					projectID,
					defaultStorage,
					&mockImageServiceErrorCheckImage{},
					&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[1]},
					logger, &test.Timeout,
					false,
				),
			},
			wantError: errors.New(
				"Replication of image \"stemcell-0.0.0-aa937100\" in zone \"zone2\": cannot find the vmimage"),
		},
		"success": {
			leaderZone: "zone1",
			zonalStemcells: map[string]*Stemcells{
				"zone1": func() *Stemcells {
					s := NewStemcells(
						test.FakeClient(createImageImport(vmconsts.ImportJobComplete)),
						projectID,
						defaultStorage,
						&mockImageService{existingImage: createVirtualMachineImageWithLabel(defaultStemcellID.AsString())},
						&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[0]},
						logger, &test.Timeout,
						false,
					)
					s.ignoreConflict = true
					return s
				}(),
				"zone2": func() *Stemcells {
					s := NewStemcells(
						test.FakeClient(createVirtualMachineImageWithLabel(defaultStemcellID.AsString())),
						projectID,
						defaultStorage,
						&mockImageService{existingImage: createVirtualMachineImageWithLabel(defaultStemcellID.AsString())},
						&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[1]},
						logger, &test.Timeout,
						false,
					)
					s.ignoreConflict = true
					return s
				}(),
			},
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			m := &MultizoneStemcells{
				leaderZone:     tc.leaderZone,
				zonalStemcells: tc.zonalStemcells,
				logger:         logger,
			}
			cid, err := m.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
			if err == nil && tc.wantError != nil {
				t.Fatalf("got nil, want: %v", tc.wantError)
			} else if err != nil && tc.wantError == nil {
				t.Fatalf("got unexpected error: %v, want: nil", err)
			} else if err == nil && id == "success" {
				for zone, stemcell := range m.zonalStemcells {
					t.Run(fmt.Sprintf("assert label on zone %s", zone), func(t *testing.T) {
						image, err := stemcell.imageService.Get(nil, cid.AsString())
						if err != nil {
							t.Fatalf("unexpected error getting image: %v", err)
						}
						if image.Labels[vmconsts.CPILabel] != cpiValue {
							t.Fatalf("expected label %s with value %s, but not found", vmconsts.CPILabel, cpiValue)
						}
					})
				}
			} else if err != nil && tc.wantError != nil && tc.wantError.Error() != err.Error() {
				t.Fatalf("got unexpected error: %v, want: %v", err, tc.wantError)
			}
		})
	}
}

func TestDeleteMultizoneStemcells(t *testing.T) {
	testCases := map[string]struct {
		leaderZone     string
		zonalStemcells map[string]*Stemcells
		wantError      error
	}{
		"error deleting one of the stemcells": {
			leaderZone: "zone1",
			zonalStemcells: map[string]*Stemcells{
				"zone1": NewStemcells(
					test.FakeClient(),
					projectID,
					defaultStorage,
					&mockImageService{existingImage: createVirtualMachineImageWithLabel(defaultStemcellID.AsString())},
					&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[0]},
					logger, &test.Timeout,
					false,
				),
				"zone2": NewStemcells(
					test.FakeClient(createVirtualMachineImageWithLabel(defaultStemcellID.AsString())),
					projectID,
					defaultStorage,
					&mockImageServiceErrorDeletion{image: createVirtualMachineImageWithLabel(defaultStemcellID.AsString())},
					&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[1]},
					logger, &test.Timeout,
					false,
				),
			},
			wantError: errors.New("Delete stemcell \"stemcell-0.0.0-aa937100\" in zone \"zone2\": " +
				"delete stemcell: failed to delete stemcell"),
		},
		"success": {
			leaderZone: "zone1",
			zonalStemcells: map[string]*Stemcells{
				"zone1": NewStemcells(
					test.FakeClient(),
					projectID,
					defaultStorage,
					&mockImageService{existingImage: createVirtualMachineImageWithLabel(defaultStemcellID.AsString())},
					&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[0]},
					logger, &test.Timeout,
					false,
				),
				"zone2": NewStemcells(
					test.FakeClient(),
					projectID,
					defaultStorage,
					&mockImageService{existingImage: createVirtualMachineImageWithLabel(defaultStemcellID.AsString())},
					&staticMultiZoneUUIDGenerator{uuid: multiZoneUUIDs[1]},
					logger, &test.Timeout,
					false,
				),
			},
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			m := &MultizoneStemcells{
				leaderZone:     tc.leaderZone,
				zonalStemcells: tc.zonalStemcells,
				logger:         logger,
			}
			cid, _ := m.CreateStemcell(defaultImagePath, defaultStemcellCloudProps)
			err := m.DeleteStemcell(cid)
			if err == nil && tc.wantError != nil {
				t.Fatalf("got nil, want: %v", tc.wantError)
			} else if err != nil && tc.wantError.Error() != err.Error() {
				t.Fatalf("got unexpected error: %v, want: %v", err, tc.wantError)
			}
		})
	}
}

// A mock implementation of 'uuid.Generator' that generates the UUID provided.
type staticMultiZoneUUIDGenerator struct {
	uuid string
}

func (g *staticMultiZoneUUIDGenerator) Generate() (string, error) {
	return g.uuid, nil
}

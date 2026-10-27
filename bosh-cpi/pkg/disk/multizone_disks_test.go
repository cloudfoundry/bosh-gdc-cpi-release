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
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/agent"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service"

	backupv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/backup/v1"

	kubeclients "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/metadata"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
)

func TestNewMultizoneDisks_MetadataServerUsage(t *testing.T) {
	testZones := []string{"zone1", "zone2"}
	testKubeClients := &kubeclients.KubeClients{
		ZonalAPIServers: map[string]client.WithWatch{
			"zone1": test.FakeClient(),
			"zone2": test.FakeClient(),
		},
	}
	agentEnvServices := &agent.AgentEnvServices{
		ZonalAgentEnvServices: map[string]agent.AgentEnvService{
			"zone1": {},
			"zone2": {},
		},
	}
	testConfig := &config.Config{
		GDC: config.GDCConfig{},
	}

	// For simplicity, metadata clients differentiated trivially by OrgSuffix.
	// NewMultizoneDisks assumes the input metadata client map is correctly configured.
	mc1 := &metadata.MetadataClient{
		ProjectID: "test-project",
		OrgSuffix: "zone-1-metadata-client",
		Client:    &http.Client{},
	}
	mc2 := &metadata.MetadataClient{
		ProjectID: "test-project",
		OrgSuffix: "zone-2-metadata-client",
		Client:    &http.Client{},
	}

	testCases := map[string]struct {
		useMetadataServer   bool
		useDiskHotplug      bool
		metadataClients     map[string]*metadata.MetadataClient
		wantMetadataClients map[string]*metadata.MetadataClient
	}{
		"metadata enabled: zonal metadata clients assigned zonalDisks": {
			useMetadataServer: true,
			metadataClients: map[string]*metadata.MetadataClient{
				"zone1": mc1,
				"zone2": mc2,
			},
			wantMetadataClients: map[string]*metadata.MetadataClient{
				"zone1": mc1,
				"zone2": mc2,
			},
		},
		"metadata disabled: no metadata clients assigned to zonalDisks": {
			useMetadataServer: false,
			metadataClients:   nil,
			wantMetadataClients: map[string]*metadata.MetadataClient{
				"zone1": nil,
				"zone2": nil,
			},
		},
		"metadata disabled: no metadata clients assigned to zonalDisks, even if provided": {
			useMetadataServer: false,
			metadataClients: map[string]*metadata.MetadataClient{
				"zone1": mc1,
				"zone2": mc2,
			},
			wantMetadataClients: map[string]*metadata.MetadataClient{
				"zone1": nil,
				"zone2": nil,
			},
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			gotMultizoneDisks := NewMultizoneDisks(
				testKubeClients,
				logger,
				uuidGen,
				agentEnvServices,
				testConfig,
				tc.useMetadataServer,
				tc.useDiskHotplug,
				tc.metadataClients,
			)
			if gotMultizoneDisks == nil {
				t.Fatalf("got nil MultizoneDisks object, but expected one")
			}
			if gotMultizoneDisks.zonalDisks == nil {
				t.Fatal("got nil zonalDisks map, but expected one")
			}

			for _, zone := range testZones {
				gotDiskCPI, ok := gotMultizoneDisks.zonalDisks[zone]
				if !ok {
					t.Fatalf("zone %q: got no zonalDisks object, but expected one", zone)
				}
				gotZonalDisk, ok := gotDiskCPI.(*Disks)
				if !ok {
					t.Fatalf(
						"zone %q: zonalDisks (ViskCPI interface) was not a *disk.Disks (struct), got: %T, want: %T",
						zone, gotZonalDisk, &Disks{},
					)
				}

				if tc.useMetadataServer {
					gotMetadataClient, ok := gotZonalDisk.metadataClient.(*metadata.MetadataClient)
					if !ok {
						t.Fatalf(
							"zone %q: Disks.metadataClient (interface) was not a *metadataclient.MetadataClient (struct), got: %T, want: %T",
							zone, gotZonalDisk.metadataClient, &metadata.MetadataClient{},
						)
					}

					// Verify each zonalDisk has correct metadata client. For testing, ID client by given OrgSuffix.
					if gotMetadataClient.OrgSuffix != tc.wantMetadataClients[zone].OrgSuffix {
						t.Errorf(
							"zone %q: zonalDisk has wrong metadata client, got %q, want: %q",
							zone, gotMetadataClient.OrgSuffix, tc.wantMetadataClients[zone].OrgSuffix,
						)
					}
				} else {
					if gotZonalDisk.metadataClient != tc.wantMetadataClients[zone] {
						t.Fatalf(
							"zone %q: got metadata client value %v, but expected nil",
							zone, gotZonalDisk.metadataClient,
						)
					}
				}
			}
		})
	}
}

func TestMultizoneGetDisks(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalDisks   map[string]DiskCPI
		error        bool
	}{
		"vm not found in any zone": {
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
		"vm found in zone1": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failGetDisks: true,
				},
			},
			error: false,
		},
		"error getting disks from zone1": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{
					failGetDisks: true,
				},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			diskIds, err := multiZoneDisk.GetDisks(apiv1.NewVMCID("vm-id"))
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
			if !tc.error && len(diskIds) != 1 {
				t.Fatalf("unexpected number of disk IDs: %v", len(diskIds))
			}
			if !tc.error && diskIds[0].AsString() != "disk-id" {
				t.Fatalf("unexpected disk ID: %v", diskIds[0])
			}
		})
	}
}

func TestCreateDisk(t *testing.T) {
	testCases := map[string]struct {
		targetZone string
		zonalDisks map[string]DiskCPI
		error      bool
	}{
		"invalid zone": {
			targetZone: "invalidZone",
			zonalDisks: make(map[string]DiskCPI),
			error:      true,
		},
		"error creating disk in zone1": {
			targetZone: "zone1",
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{
					failDiskCreation: true,
				},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
		"successfully created disk in zone1": {
			targetZone: "zone1",
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: false,
		},
		"successfully created disk in zone2": {
			targetZone: "zone2",
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: false,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			diskSize := 100
			cloudProps := apiv1.CloudPropsImpl{
				RawMessage: []byte(fmt.Sprintf(`{"zone":"%s"}`, tc.targetZone)),
			}
			vmCID := apiv1.VMCID{}
			multiZoneDisk := MultizoneDisks{
				zonalDisks: tc.zonalDisks,
			}
			diskCID, err := multiZoneDisk.CreateDisk(diskSize, cloudProps, &vmCID)
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
			if !tc.error && diskCID.AsString() != "disk-id" {
				t.Fatalf("unexpected disk CID: %v", diskCID)
			}
		})
	}
}

func TestCreateDisk_WithAssociatedVMCID(t *testing.T) {
	testCases := map[string]struct {
		zonalDisks   map[string]DiskCPI
		zone1Objects []client.Object
		zone2Objects []client.Object
		error        bool
	}{
		"vm found": {
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failDiskCreation: true,
				},
			},
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			error:        false,
		},
		"vm not found": {
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			diskSize := 100
			cloudProps := apiv1.CloudPropsImpl{
				RawMessage: []byte(`{}`),
			}
			vmCID := apiv1.NewVMCID("vm-id")
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			diskCID, err := multiZoneDisk.CreateDisk(diskSize, cloudProps, &vmCID)
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
			if !tc.error && diskCID.AsString() != "disk-id" {
				t.Fatalf("unexpected disk CID: %v", diskCID)
			}
		})
	}
}

func TestDeleteDisk(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalDisks   map[string]DiskCPI
		error        bool
	}{
		"disk not found in any zone": {
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: false,
		},
		"successfully deleted disk in zone-1": {
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failDiskDeletion: true,
				},
			},
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			error:        false,
		},
		"successfully deleted disk in zone-2": {
			zonalDisks: map[string]DiskCPI{
				"zone2": &mockZonalDisk{},
				"zone1": &mockZonalDisk{
					failDiskDeletion: true,
				},
			},
			zone2Objects: []client.Object{virtualMachineDisk("disk-id")},
			error:        false,
		},
		"error deleting disk in zone-1": {
			zonalDisks: map[string]DiskCPI{
				"zone2": &mockZonalDisk{},
				"zone1": &mockZonalDisk{
					failDiskDeletion: true,
				},
			},
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			error:        true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneDisk.DeleteDisk(apiv1.NewDiskCID("disk-id"))
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestAttachDisk(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalDisks   map[string]DiskCPI
		error        bool
	}{
		"disk not found": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
		"vm not found": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
		"disk and vm not in same zone": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zone2Objects: []client.Object{virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
		"disk attachment successful in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id"), virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failDiskAttach: true,
				},
			},
			error: false,
		},
		"disk attachment failed in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id"), virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{
					failDiskAttach: true,
				},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneDisk.AttachDisk(apiv1.NewVMCID("vm-id"), apiv1.NewDiskCID("disk-id"))
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestDetachDisk(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalDisks   map[string]DiskCPI
		error        bool
	}{
		"disk not found": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
		"vm not found": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
		"disk and vm not in same zone": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zone2Objects: []client.Object{virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
		"disk detachment successful in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id"), virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failDiskDetach: true,
				},
			},
			error: false,
		},
		"disk detachment failed in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id"), virtualMachine("vm-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{
					failDiskDetach: true,
				},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneDisk.DetachDisk(apiv1.NewVMCID("vm-id"), apiv1.NewDiskCID("disk-id"))
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestMultizoneHasDisk(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		found        bool
	}{
		"disk not found in any zone": {
			found: false,
		},
		"disk found in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			found:        true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			hasDisk, _ := multiZoneDisk.HasDisk(apiv1.NewDiskCID("disk-id"))
			if tc.found != hasDisk {
				t.Fatalf("expected %v but got %v", tc.found, hasDisk)
			}
		})
	}
}

func TestMultizoneResizeDisk(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalDisks   map[string]DiskCPI
		error        bool
	}{
		"disk not found": {
			error: true,
		},
		"resize disk in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failDiskResize: true,
				},
			},
			error: false,
		},
		"resize disk failed": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{
					failDiskResize: true,
				},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneDisk.ResizeDisk(apiv1.NewDiskCID("disk-id"), 200)
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestSetDiskMetadata(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalDisks   map[string]DiskCPI
		error        bool
	}{
		"disk not found": {
			error: true,
		},
		"setting disk metadata in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failDiskSetMetadata: true,
				},
			},
			error: false,
		},
		"setting disk metadata  failed": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{
					failDiskSetMetadata: true,
				},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneDisk.SetDiskMetadata(apiv1.NewDiskCID("disk-id"), apiv1.DiskMeta{})
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestSnapshotDisk(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalDisks   map[string]DiskCPI
		error        bool
	}{
		"disk not found": {
			error: true,
		},
		"disk snapshot succeeded in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failDiskSnapshot: true,
				},
			},
			error: false,
		},
		"disk snapshot failed in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{
					failDiskSnapshot: true,
				},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			snapshotId, err := multiZoneDisk.SnapshotDisk(apiv1.NewDiskCID("disk-id"), apiv1.DiskMeta{})
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
			if !tc.error && snapshotId.AsString() != "snapshot-id" {
				t.Fatalf("unexpected snapshot ID: %v", snapshotId)
			}
		})
	}
}

func TestDeleteSnapshot(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalDisks   map[string]DiskCPI
		error        bool
	}{
		"snapshot not found": {
			error: true,
		},
		"delete snapshot succeeded in zone1": {
			zone1Objects: []client.Object{diskBackup("snapshot-id", backupv1.BackupStateSucceeded)},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{},
				"zone2": &mockZonalDisk{
					failDeleteSnapshot: true,
				},
			},
			error: false,
		},
		"delete snapshot failed in zone1": {
			zone1Objects: []client.Object{diskBackup("snapshot-id", backupv1.BackupStateSucceeded)},
			zonalDisks: map[string]DiskCPI{
				"zone1": &mockZonalDisk{
					failDeleteSnapshot: true,
				},
				"zone2": &mockZonalDisk{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1KubeClient := test.FakeClient(tc.zone1Objects...)
			zone2KubeClient := test.FakeClient(tc.zone2Objects...)
			clients := &kubeclients.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1KubeClient,
					"zone2": zone2KubeClient,
				},
			}
			multiZoneDisk := MultizoneDisks{
				logger:          logger,
				zonalDisks:      tc.zonalDisks,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneDisk.DeleteSnapshot(apiv1.NewSnapshotCID("snapshot-id"))
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

type mockZonalDisk struct {
	failGetDisks        bool
	failDiskCreation    bool
	failDiskDeletion    bool
	failDiskAttach      bool
	failDiskDetach      bool
	failDiskSetMetadata bool
	failDiskResize      bool
	diskExists          bool
	failDiskSnapshot    bool
	failDeleteSnapshot  bool
}

func (md mockZonalDisk) GetDisks(vmID apiv1.VMCID) ([]apiv1.DiskCID, error) {
	if md.failGetDisks {
		return nil, errors.New("failed to get disks")
	}

	return []apiv1.DiskCID{apiv1.NewDiskCID("disk-id")}, nil
}

func (md mockZonalDisk) CreateDisk(size int, cloudProps apiv1.DiskCloudProps, associatedVMCID *apiv1.VMCID) (apiv1.DiskCID, error) {
	if md.failDiskCreation {
		return apiv1.DiskCID{}, errors.New("failed to create disk")
	}

	return apiv1.NewDiskCID("disk-id"), nil
}

func (md mockZonalDisk) DeleteDisk(cid apiv1.DiskCID) error {
	if md.failDiskDeletion {
		return errors.New("failed to delete disk")
	}

	return nil
}

func (md mockZonalDisk) AttachDisk(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) error {
	if md.failDiskAttach {
		return errors.New("failed to attach disk")
	}

	return nil
}

func (md mockZonalDisk) AttachDiskV2(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) (apiv1.DiskHint, error) {
	if md.failDiskAttach {
		return apiv1.DiskHint{}, errors.New("failed to attach disk")
	}

	return apiv1.DiskHint{}, nil
}

func (md mockZonalDisk) DetachDisk(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) error {
	if md.failDiskDetach {
		return errors.New("failed to detach disk")
	}

	return nil
}

func (md mockZonalDisk) SetDiskMetadata(cid apiv1.DiskCID, metadata apiv1.DiskMeta) error {
	if md.failDiskSetMetadata {
		return errors.New("failed to set disk metadata")
	}

	return nil
}

func (md mockZonalDisk) ResizeDisk(cid apiv1.DiskCID, size int) error {
	if md.failDiskResize {
		return errors.New("failed to resize disk")
	}

	return nil
}

func (md mockZonalDisk) HasDisk(cid apiv1.DiskCID) (bool, error) {
	return md.diskExists, nil
}

func (md mockZonalDisk) SnapshotDisk(cid apiv1.DiskCID, meta apiv1.DiskMeta) (apiv1.SnapshotCID, error) {
	if md.failDiskSnapshot {
		return apiv1.SnapshotCID{}, errors.New("failed to snapshot disk")
	}
	return apiv1.NewSnapshotCID("snapshot-id"), nil
}

func (md mockZonalDisk) DeleteSnapshot(cid apiv1.SnapshotCID) error {
	if md.failDeleteSnapshot {
		return errors.New("failed to delete snapshot")
	}
	return nil
}

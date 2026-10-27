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

package vm

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/agent"
	kubeclients "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/metadata"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service"
	vmservice "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/vm"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
)

func TestNewMultizoneVMs_MetadataServerUsage(t *testing.T) {
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
	// NewMultizoneVMs assumes the input metadata client map is correctly configured.
	metadataClientZone1 := &metadata.MetadataClient{
		ProjectID: "test-project",
		OrgSuffix: "zone-1-metadata-client",
		Client:    &http.Client{},
	}
	metadataClientZone2 := &metadata.MetadataClient{
		ProjectID: "test-project",
		OrgSuffix: "zone-2-metadata-client",
		Client:    &http.Client{},
	}

	testCases := map[string]struct {
		useMetadataServer   bool
		metadataClients     map[string]*metadata.MetadataClient
		wantMetadataClients map[string]*metadata.MetadataClient
		useDiskHotplug      bool
		useCloudNATGateways bool
		useTier1Network     bool
	}{
		"metadata enabled: zonal metadata clients assigned zonalVMs": {
			useMetadataServer: true,
			metadataClients: map[string]*metadata.MetadataClient{
				"zone1": metadataClientZone1,
				"zone2": metadataClientZone2,
			},
			wantMetadataClients: map[string]*metadata.MetadataClient{
				"zone1": metadataClientZone1,
				"zone2": metadataClientZone2,
			},
			useDiskHotplug: false,
		},
		"metadata disabled: no metadata clients assigned to zonalVMs": {
			useMetadataServer: false,
			metadataClients:   nil,
			wantMetadataClients: map[string]*metadata.MetadataClient{
				"zone1": nil,
				"zone2": nil,
			},
		},
		"metadata disabled: no metadata clients assigned to zonalVMs, even if provided": {
			useMetadataServer: false,
			metadataClients: map[string]*metadata.MetadataClient{
				"zone1": metadataClientZone1,
				"zone2": metadataClientZone2,
			},
			wantMetadataClients: map[string]*metadata.MetadataClient{
				"zone1": nil,
				"zone2": nil,
			},
			useDiskHotplug:      false,
			useCloudNATGateways: false,
			useTier1Network:     false,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			gotMultizoneVMs := NewMultizoneVMs(
				testKubeClients,
				logger,
				uuidGen,
				agentOpt,
				agentEnvServices,
				testConfig,
				tc.useMetadataServer,
				tc.metadataClients,
				tc.useDiskHotplug,
				tc.useCloudNATGateways,
				tc.useTier1Network,
				nil,
			)
			if gotMultizoneVMs == nil {
				t.Fatalf("got nil multizone vm object, but expected one")
			}
			if gotMultizoneVMs.zonalVMs == nil {
				t.Fatal("got nil zonalVMs map, but expected one")
			}

			for _, zone := range testZones {
				gotVMCPI, ok := gotMultizoneVMs.zonalVMs[zone]
				if !ok {
					t.Fatalf("zone %q: got no zonalVM, but expected one", zone)
				}
				gotZonalVM, ok := gotVMCPI.(*VMs)
				if !ok {
					t.Fatalf(
						"zone %q: zonalVM (VMCPI interface) was not a *vm.VMs (struct), got: %T, want: %T",
						zone, gotZonalVM, &VMs{},
					)
				}

				if tc.useMetadataServer {
					gotMetadataClient, ok := gotZonalVM.metadataClient.(*metadata.MetadataClient)
					if !ok {
						t.Fatalf(
							"zone %q: VM.metadataClient (interface) was not a *metadataclient.MetadataClient (struct), got: %T, want: %T",
							zone, gotZonalVM.metadataClient, &metadata.MetadataClient{},
						)
					}

					// Verify each zonalVM has correct metadata client. For testing, ID client by given OrgSuffix.
					if gotMetadataClient.OrgSuffix != tc.wantMetadataClients[zone].OrgSuffix {
						t.Errorf(
							"zone %q: zonalVM has wrong metadata client, got %q, want: %q",
							zone, gotMetadataClient.OrgSuffix, tc.wantMetadataClients[zone].OrgSuffix,
						)
					}
				} else {
					if gotZonalVM.metadataClient != tc.wantMetadataClients[zone] {
						t.Fatalf(
							"zone %q: got metadata client value %v, but expected nil",
							zone, gotZonalVM.metadataClient,
						)
					}
				}
			}
		})
	}
}

func TestNewMultizoneVMs_DiskHotplugUsage(t *testing.T) {
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
	metadataClients := map[string]*metadata.MetadataClient{
		"zone1": &metadata.MetadataClient{},
		"zone2": &metadata.MetadataClient{},
	}

	testCases := map[string]struct {
		useDiskHotplug  bool
		wantDiskHotplug bool
	}{
		"disk hotplug enabled": {
			useDiskHotplug:  true,
			wantDiskHotplug: true,
		},
		"disk hotplug disabled": {
			useDiskHotplug:  false,
			wantDiskHotplug: false,
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			gotMultizoneVMs := NewMultizoneVMs(
				testKubeClients,
				logger,
				uuidGen,
				agentOpt,
				agentEnvServices,
				testConfig,
				false,
				metadataClients,
				tc.useDiskHotplug,
				false,
				false,
				nil,
			)
			if gotMultizoneVMs == nil {
				t.Fatalf("NewMultizoneVMs() got nil, but expected a MultizoneVMs object")
			}
			if gotMultizoneVMs.zonalVMs == nil {
				t.Fatal("NewMultizoneVMs() got nil zonalVMs map, but expected one")
			}

			for _, zone := range testZones {
				gotVMCPI, ok := gotMultizoneVMs.zonalVMs[zone]
				if !ok {
					t.Fatalf("zone %q: got no zonalVM, but expected one", zone)
				}
				gotZonalVM, ok := gotVMCPI.(*VMs)
				if !ok {
					t.Fatalf(
						"zone %q: zonalVM (VMCPI interface) was not a *vm.VMs (struct), got: %T, want: %T",
						zone, gotZonalVM, &VMs{},
					)
				}

				if gotZonalVM.useDiskHotplug != tc.wantDiskHotplug {
					t.Errorf("zone %q: zonalVM.useDiskHotplug got %v, want %v", zone, gotZonalVM.useDiskHotplug, tc.wantDiskHotplug)
				}
			}
		})
	}
}

func TestNewMultizoneVMs_VMARServiceDistribution(t *testing.T) {
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
	vmAccessServices := map[string]vmservice.VMARAccessChecker{
		"zone1": &mockVMAccessChecker{zone: "zone1"},
		"zone2": &mockVMAccessChecker{zone: "zone2"},
	}

	gotMultizoneVMs := NewMultizoneVMs(
		testKubeClients,
		logger,
		uuidGen,
		agentOpt,
		agentEnvServices,
		testConfig,
		false,
		nil,
		false,
		false,
		false,
		vmAccessServices,
	)

	for _, zone := range testZones {
		vmCPI, ok := gotMultizoneVMs.zonalVMs[zone]
		if !ok {
			t.Fatalf("zone %q: got no zonalVM, but expected one", zone)
		}
		zonalVM, ok := vmCPI.(*VMs)
		if !ok {
			t.Fatalf("zone %q: zonalVM was not of expected type *VMs", zone)
		}

		if zonalVM.vmAccessService == nil {
			t.Fatalf("zone %q: got nil zonalVM.vmAccessService, but expected an instance", zone)
		}

		mockVmar, ok := zonalVM.vmAccessService.(*mockVMAccessChecker)
		if !ok {
			t.Fatalf("zone %q: zonalVM.vmAccessService was not of expected type *mockVMAccessChecker", zone)
		}

		if mockVmar.zone != zone {
			t.Errorf("zone %q: zonalVM.vmAccessService.zone got %q, want %q", zone, mockVmar.zone, zone)
		}
	}
}

func TestCreateVM(t *testing.T) {
	testCases := map[string]struct {
		targetZone string
		zonalVMs   map[string]VMCPI
		error      bool
	}{
		"invalid zone": {
			targetZone: "invalidZone",
			zonalVMs:   make(map[string]VMCPI),
			error:      true,
		},
		"successfully created vm in zone1": {
			targetZone: "zone1",
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{},
				"zone2": &mockZonalVM{
					failCreateVM: true,
				},
			},
			error: false,
		},
		"failed to create vm in zone1": {
			targetZone: "zone1",
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{
					failCreateVM: true,
				},
				"zone2": &mockZonalVM{},
			},
			error: true,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			cloudProps := apiv1.CloudPropsImpl{
				RawMessage: []byte(fmt.Sprintf(`{"zone":"%s"}`, tc.targetZone)),
			}
			multiZoneVM := MultizoneVMs{
				zonalVMs: tc.zonalVMs,
			}
			vmCID, err := multiZoneVM.CreateVM(apiv1.AgentID{},
				apiv1.StemcellCID{},
				cloudProps,
				apiv1.Networks{},
				[]apiv1.DiskCID{},
				apiv1.VMEnv{})
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
			if !tc.error && vmCID.AsString() != "vm-id" {
				t.Fatalf("unexpected vm CID: %v", vmCID)
			}
		})
	}
}

func TestDeleteVM(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalVMs     map[string]VMCPI
		error        bool
	}{
		"vm not found in any zone": {
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{},
				"zone2": &mockZonalVM{},
			},
			error: false,
		},
		"successfully deleted vm in zone-1": {
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{},
				"zone2": &mockZonalVM{
					failDeleteVM: true,
				},
			},
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			error:        false,
		},
		"error deleting vm in zone-1": {
			zonalVMs: map[string]VMCPI{
				"zone2": &mockZonalVM{},
				"zone1": &mockZonalVM{
					failDeleteVM: true,
				},
			},
			zone1Objects: []client.Object{virtualMachine("vm-id")},
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
			multiZoneVM := MultizoneVMs{
				logger:          logger,
				zonalVMs:        tc.zonalVMs,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneVM.DeleteVM(apiv1.NewVMCID("vm-id"))
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestMultizoneHasVM(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		found        bool
	}{
		"vm not found in any zone": {
			found: false,
		},
		"vm found in zone1": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
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
			multiZoneVM := MultizoneVMs{
				logger:          logger,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			hasVM, _ := multiZoneVM.HasVM(apiv1.NewVMCID("vm-id"))
			if tc.found != hasVM {
				t.Fatalf("expected %v but got %v", tc.found, hasVM)
			}
		})
	}
}

func TestSetVMMetadata(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalVMs     map[string]VMCPI
		error        bool
	}{
		"vm not found in any zone": {
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{},
				"zone2": &mockZonalVM{},
			},
			error: true,
		},
		"setting vm metadata is successful in zone1": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{},
				"zone2": &mockZonalVM{
					failSetVMMetadata: true,
				},
			},
			error: false,
		},
		"failed to set vm metadata in zone1": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{
					failSetVMMetadata: true,
				},
				"zone2": &mockZonalVM{},
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
			multiZoneVM := MultizoneVMs{
				logger:          logger,
				zonalVMs:        tc.zonalVMs,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneVM.SetVMMetadata(apiv1.NewVMCID("vm-id"), apiv1.VMMeta{})
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestRebootVM(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		zonalVMs     map[string]VMCPI
		error        bool
	}{
		"vm not found in any zone": {
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{},
				"zone2": &mockZonalVM{},
			},
			error: true,
		},
		"rebooting vm is successful in zone1": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{},
				"zone2": &mockZonalVM{
					failRebootVM: true,
				},
			},
			error: false,
		},
		"rebooting vm failed in zone1": {
			zone1Objects: []client.Object{virtualMachine("vm-id")},
			zonalVMs: map[string]VMCPI{
				"zone1": &mockZonalVM{
					failRebootVM: true,
				},
				"zone2": &mockZonalVM{},
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
			multiZoneVM := MultizoneVMs{
				logger:          logger,
				zonalVMs:        tc.zonalVMs,
				multizoneFinder: service.NewMultizoneFinder(clients, projectID),
			}
			err := multiZoneVM.RebootVM(apiv1.NewVMCID("vm-id"))
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestMZCalculateVMCloudProperties(t *testing.T) {
	testCases := map[string]struct {
		multizoneVM MultizoneVMs
		error       bool
	}{
		"leader zone not found": {
			error: true,
		},
		"successfully returned vm cloud properties": {
			multizoneVM: MultizoneVMs{
				zonalVMs: map[string]VMCPI{
					"zone1": &mockZonalVM{},
					"zone2": &mockZonalVM{},
				},
				leaderZone: "zone1",
			},
			error: false,
		},
	}
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			_, err := tc.multizoneVM.CalculateVMCloudProperties(apiv1.VMResources{})
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
		})
	}
}

func TestFindingLeaderZone(t *testing.T) {
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
	multiZoneVMs := NewMultizoneVMs(&clients, nil, nil, apiv1.AgentOptions{}, nil, &config, false, nil, false, false, false, nil)
	if multiZoneVMs.leaderZone != "zone1" {
		t.Fatalf("expected leader zone to be zone1 but got %s", multiZoneVMs.leaderZone)
	}
}

type mockZonalVM struct {
	failCreateVM              bool
	failDeleteVM              bool
	vmExists                  bool
	failRebootVM              bool
	failCalculateVMCloudProps bool
	failSetVMMetadata         bool
}

func (vm *mockZonalVM) CreateVM(apiv1.AgentID, apiv1.StemcellCID, apiv1.VMCloudProps, apiv1.Networks, []apiv1.DiskCID, apiv1.VMEnv) (apiv1.VMCID, error) {
	if vm.failCreateVM {
		return apiv1.VMCID{}, errors.New("failed to create vm")
	}
	return apiv1.NewVMCID("vm-id"), nil
}

func (vm *mockZonalVM) CreateVMV2(apiv1.AgentID, apiv1.StemcellCID, apiv1.VMCloudProps, apiv1.Networks, []apiv1.DiskCID, apiv1.VMEnv) (apiv1.VMCID, apiv1.Networks, error) {
	if vm.failCreateVM {
		return apiv1.VMCID{}, apiv1.Networks{}, errors.New("failed to create vm")
	}
	return apiv1.NewVMCID("vm-id"), apiv1.Networks{}, nil
}

func (vm *mockZonalVM) DeleteVM(cid apiv1.VMCID) error {
	if vm.failDeleteVM {
		return errors.New("failed to delete vm")
	}
	return nil
}

func (vm *mockZonalVM) CalculateVMCloudProperties(apiv1.VMResources) (apiv1.VMCloudProps, error) {
	if vm.failCalculateVMCloudProps {
		return apiv1.VMCloudPropsImpl{}, errors.New("failed to calculate vm cloud props")
	}
	return apiv1.VMCloudPropsImpl{}, nil
}

func (vm *mockZonalVM) SetVMMetadata(apiv1.VMCID, apiv1.VMMeta) error {
	if vm.failSetVMMetadata {
		return errors.New("failed to set vm metadata")
	}
	return nil
}

func (vm *mockZonalVM) HasVM(apiv1.VMCID) (bool, error) {
	return vm.vmExists, nil
}

func (vm *mockZonalVM) RebootVM(apiv1.VMCID) error {
	if vm.failRebootVM {
		return errors.New("failed to reboot vm")
	}
	return nil
}

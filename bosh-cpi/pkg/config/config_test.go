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

package config

import (
	"os"
	"testing"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	"github.com/cloudfoundry/bosh-utils/system"
	"github.com/google/go-cmp/cmp"
)

var (
	logger         = boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr)
	fs             = system.NewOsFileSystem(logger)
	defaultTimeout = WaitTimeout{
		DiskCreation:        45,
		DiskDeletion:        2,
		HotDiskAttachment:   5,
		HotDiskDetachment:   5,
		DiskResize:          5,
		SnapshotCreation:    10,
		SnapshotDeletion:    5,
		StemcellCreation:    150,
		StemcellDeletion:    5,
		StemcellReplication: 2,
		VMDeletion:          2,
		VMStart:             30,
		VMStop:              5,
	}
)

func TestInvalidConfig(t *testing.T) {
	saConfig := serviceAccountKey()

	testCases := map[string]struct {
		config *Config
	}{
		"empty org": {
			config: &Config{
				GDC: GDCConfig{
					OrgAdminURL:       "url",
					ServiceAccountKey: *saConfig,
					CAData:            "caData",
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty zonal APIs map when leader zone is specified": {
			config: &Config{
				GDC: GDCConfig{
					Org:               "org",
					ServiceAccountKey: *saConfig,
					CAData:            "caData",
					LeaderZone:        "zone1",
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"missing config in zonal APIs map associated with leader zone": {
			config: &Config{
				GDC: GDCConfig{
					Org:               "org",
					ServiceAccountKey: *saConfig,
					CAData:            "caData",
					LeaderZone:        "zone1",
					OrgAPIEndpoints: OrgAPIConfig{
						ZonalAPIs: map[string]string{
							"zone2": "https://management-kube.apiserver.org.zone2.test",
						},
					},
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty org admin url": {
			config: &Config{
				GDC: GDCConfig{
					Org:               "org",
					ServiceAccountKey: *saConfig,
					CAData:            "caData",
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty sa name": {
			config: &Config{
				GDC: GDCConfig{
					Org:         "org",
					OrgAdminURL: "url",
					CAData:      "caData",
					ServiceAccountKey: ServiceAccountKeyConfig{
						Project:      "project",
						TokenURI:     "token_uri",
						PrivateKeyID: "1234",
						PrivateKey:   "key",
					},
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty project": {
			config: &Config{
				GDC: GDCConfig{
					Org:         "org",
					OrgAdminURL: "url",
					CAData:      "caData",
					ServiceAccountKey: ServiceAccountKeyConfig{
						ServiceAccountName: "name",
						TokenURI:           "token_uri",
						PrivateKeyID:       "1234",
						PrivateKey:         "key",
					},
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty token uri": {
			config: &Config{
				GDC: GDCConfig{
					Org:         "org",
					OrgAdminURL: "url",
					CAData:      "caData",
					ServiceAccountKey: ServiceAccountKeyConfig{
						ServiceAccountName: "name",
						Project:            "project",
						PrivateKeyID:       "1234",
						PrivateKey:         "key",
					},
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty private key id": {
			config: &Config{
				GDC: GDCConfig{
					Org:         "org",
					OrgAdminURL: "url",
					CAData:      "caData",
					ServiceAccountKey: ServiceAccountKeyConfig{
						ServiceAccountName: "name",
						Project:            "project",
						TokenURI:           "token_uri",
						PrivateKey:         "key",
					},
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty private key": {
			config: &Config{
				GDC: GDCConfig{
					Org:         "org",
					OrgAdminURL: "url",
					CAData:      "caData",
					ServiceAccountKey: ServiceAccountKeyConfig{
						ServiceAccountName: "name",
						Project:            "project",
						PrivateKeyID:       "1234",
						TokenURI:           "token_uri",
					},
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty ca data": {
			config: &Config{
				GDC: GDCConfig{
					Org:               "org",
					ServiceAccountKey: *saConfig,
					OrgAdminURL:       "url",
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty storage cred access key ID": {
			&Config{
				GDC: GDCConfig{
					ServiceAccountKey: *saConfig,
					CAData:            "caData",
					StorageCredential: S3Credential{
						AccessKey: "access_key",
					},
					OrgAdminURL: "url",
					Org:         "org",
					ExternalAccessPorts: []ServerPort{
						{
							Name:     "port_name",
							Protocol: "TCP",
							Port:     1234,
						},
					},
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
		"empty storage cred access key": {
			&Config{
				GDC: GDCConfig{
					ServiceAccountKey: *saConfig,
					CAData:            "caData",
					StorageCredential: S3Credential{
						AccessKeyID: "access_key_id",
					},
					OrgAdminURL: "url",
					Org:         "org",
					ExternalAccessPorts: []ServerPort{
						{
							Name:     "port_name",
							Protocol: "TCP",
							Port:     1234,
						},
					},
				},
				Agent: apiv1.AgentOptions{
					Mbus: "https://user:password@127.0.0.1:4321/agent",
				},
			},
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			if err := tc.config.Validate(); err == nil {
				t.Error("got nil but expecting an error")
			}
		})
	}
}

func TestValidConfig(t *testing.T) {
	saConfig := serviceAccountKey()

	validConfig := &Config{
		GDC: GDCConfig{
			ServiceAccountKey: *saConfig,
			CAData:            "caData",
			StorageCredential: S3Credential{
				AccessKeyID: "access_key_id",
				AccessKey:   "access_key",
			},
			OrgAdminURL: "url",
			OrgAPIEndpoints: OrgAPIConfig{
				GlobalAPI: "https://xxx-kube.org-name.global.xxx.xxx",
			},
			Org: "org",
			ExternalAccessPorts: []ServerPort{
				{
					Name:     "port_name",
					Protocol: "TCP",
					Port:     1234,
				},
			},
		},
		Agent: apiv1.AgentOptions{
			Mbus: "https://user:password@127.0.0.1:4321/agent",
		},
	}
	if err := validConfig.Validate(); err != nil {
		t.Errorf("expecting nil error but got %v", err)
	}
}

func TestNewConfigFromPath(t *testing.T) {
	cpiConfigString := `
	{
		"gdch": {
			"org": "org",
			"orgAdminURL": "org_url",
			"orgAPIEndpoints": {
				"globalAPI": "https://xxx-kube.org-name.global.xxx.xxx"
			},
			"serviceAccountPKConfig": {
				"serviceAccountName": "user-sa",
				"project": "user-proj",
				"tokenUri": "token_url",
				"privateKeyID": "private_key_id",
				"privateKey": "private_key"
			},
			"caData": "encoded_ca",
			"storageCredential": {
				"accessKeyID": "access_key_id",
				"accessKey": "access_key"
			},
			"externalAccessPorts": [
				{
					"name": "port_name",
					"protocol": "TCP",
					"port": 1234
				}
			],
			"imageProject": "",
			"disableSSHProxy": true,
			"useTmpDirForSSHKey": true,
			"boshEnvSettingPath": "",
			"useCloudNATGateways": true,
			"useTier1Network": true,
			"timeout": {
				"diskCreation": 90
			}
		},
		"agent": {
			"mbus": "mbus_url",
			"ntp": []
		}
	}`

	cpiConfigFile, err := os.CreateTemp(os.TempDir(), "test_cpi_config.json")
	if err != nil {
		t.Fatalf("create CPI config file: %v", err)
	}
	defer cpiConfigFile.Close()
	defer os.Remove(cpiConfigFile.Name())

	_, err = cpiConfigFile.WriteString(cpiConfigString)
	if err != nil {
		t.Fatalf("write CPI config file: %v", err)
	}

	config, err := NewConfigFromPath(cpiConfigFile.Name(), fs)
	if err != nil {
		t.Fatalf("parse cpi config %s: %v", cpiConfigFile.Name(), err)
	}

	expectedPorts := []ServerPort{
		{
			Name:     "port_name",
			Protocol: "TCP",
			Port:     1234,
		},
	}
	if !config.GDC.UseTmpDirForSSHKey {
		t.Fatalf("expected %q is set as true", "useTmpDirForSSHKey")
	}
	if !config.GDC.UseCloudNATGateways {
		t.Fatalf("expected %q is set as true", "useCloudNATGateways")
	}
	if !config.GDC.UseTier1Network {
		t.Fatalf("expected %q is set as true", "useTier1Network")
	}
	if diff := cmp.Diff(expectedPorts, config.GDC.ExternalAccessPorts); diff != "" {
		t.Fatalf("ExternalAccessPorts diff (-want +got):\n%s", diff)
	}
	if config.GDC.Timeout.DiskCreation != 90 {
		t.Fatalf("got disk creatiom timeout: %v, expect: %v", config.GDC.Timeout.DiskCreation, 90)
	}
}

func TestDefaulting(t *testing.T) {
	testCases := map[string]struct {
		expected *Config
		input    *Config
	}{
		"Set default value for ImageProject": {
			expected: &Config{
				GDC: GDCConfig{
					ServiceAccountKey: ServiceAccountKeyConfig{
						Project: "foo",
					},
					ImageProject: "foo",
					IsMultizone:  true,
					Timeout:      defaultTimeout,
				},
			},
			input: &Config{
				GDC: GDCConfig{
					ServiceAccountKey: ServiceAccountKeyConfig{
						Project: "foo",
					},
					ImageProject: "",
					IsMultizone:  true,
				},
			},
		},
		"Set specified value for ImageProject": {
			expected: &Config{
				GDC: GDCConfig{
					ServiceAccountKey: ServiceAccountKeyConfig{
						Project: "foo",
					},
					ImageProject: "bar",
					IsMultizone:  true,
					Timeout:      defaultTimeout,
				},
			},
			input: &Config{
				GDC: GDCConfig{
					ServiceAccountKey: ServiceAccountKeyConfig{
						Project: "foo",
					},
					ImageProject: "bar",
					IsMultizone:  true,
				},
			},
		},
		"Set leader zone infra-MP API endpoint when leader zone is specified": {
			expected: &Config{
				GDC: GDCConfig{
					LeaderZone: "zone1",
					OrgAPIEndpoints: OrgAPIConfig{
						ZonalAPIs: map[string]string{
							"zone1": "https://management-kube.apiserver.org.zone1.test",
						},
					},
					OrgAdminURL: "https://management-kube.apiserver.org.zone1.test",
					IsMultizone: true,
					Timeout:     defaultTimeout,
				},
			},
			input: &Config{
				GDC: GDCConfig{
					LeaderZone: "zone1",
					OrgAPIEndpoints: OrgAPIConfig{
						ZonalAPIs: map[string]string{
							"zone1": "https://management-kube.apiserver.org.zone1.test",
						},
					},
					IsMultizone: true,
				},
			},
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			setDefaultValues(tc.input)
			if diff := cmp.Diff(tc.expected, tc.input); diff != "" {
				t.Errorf("unexpected result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExplicitlyFalseIsMultizone(t *testing.T) {
	cpiConfigString := `
	{
		"gdch": {
			"org": "org",
			"orgAdminURL": "org_url",
			"serviceAccountPKConfig": {
				"serviceAccountName": "user-sa",
				"project": "user-proj",
				"tokenUri": "token_url",
				"privateKeyID": "private_key_id",
				"privateKey": "private_key"
			},
			"caData": "encoded_ca",
			"storageCredential": {
				"accessKeyID": "access_key_id",
				"accessKey": "access_key"
			},
			"isMultiZone": false
		},
		"agent": {
			"mbus": "mbus_url"
		}
	}`

	cpiConfigFile, err := os.CreateTemp(os.TempDir(), "test_cpi_config_false.json")
	if err != nil {
		t.Fatalf("create CPI config file: %v", err)
	}
	defer cpiConfigFile.Close()
	defer os.Remove(cpiConfigFile.Name())

	_, err = cpiConfigFile.WriteString(cpiConfigString)
	if err != nil {
		t.Fatalf("write CPI config file: %v", err)
	}

	config, err := NewConfigFromPath(cpiConfigFile.Name(), fs)
	if err != nil {
		t.Fatalf("parse cpi config %s: %v", cpiConfigFile.Name(), err)
	}

	if config.GDC.IsMultizone {
		t.Fatalf("expected isMultiZone to be false, but got true")
	}
}

func serviceAccountKey() *ServiceAccountKeyConfig {
	return &ServiceAccountKeyConfig{
		ServiceAccountName: "name",
		Project:            "project",
		TokenURI:           "token_uri",
		PrivateKeyID:       "1234",
		PrivateKey:         "key",
	}
}

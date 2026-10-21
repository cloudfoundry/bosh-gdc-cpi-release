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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apiv1 "github.com/cloudfoundry/bosh-cpi-go/apiv1"
	boshsys "github.com/cloudfoundry/bosh-utils/system"
)

type Config struct {
	GDC   GDCConfig          `json:"gdch"`
	Agent apiv1.AgentOptions `json:"agent"`
}

type GDCConfig struct {
	// Name of the org
	Org string `json:"org"`

	// The login URL of the admin cluster of the 'Org'
	OrgAdminURL string `json:"orgAdminURL"`

	// The map of global or zone to the associated API server endpoint on GDCH org infra cluster
	OrgAPIEndpoints OrgAPIConfig `json:"orgAPIEndpoints"`

	// Config of GDCH service identity private key
	ServiceAccountKey ServiceAccountKeyConfig `json:"serviceAccountPKConfig"`

	// Base64 encoded certificateAuthorityData of org admin
	CAData string `json:"caData"`

	// GDCH object storage credential associated with service account
	StorageCredential S3Credential `json:"storageCredential"`

	// The GDCH project in which the stemcells and VM images are stored.
	// When set to `vm-system`, the CPI uses GDCH golden images instead of
	// stemcells. This should only be used for testing.
	ImageProject string `json:"imageProject,omitempty"`

	// Ports need to be configured for out of GDCH network accessibility against VM's external ingress IP.
	// If empty, no external accessibility will be configured on provisioned VM.
	ExternalAccessPorts []ServerPort `json:"externalAccessPorts,omitempty"`

	// When set to true, configure to enable hot disk expansion without needing to turn off the VM when resizeDisk is called.
	EnableHotDiskExpansion bool `json:"enableHotDiskExpansion,omitempty"`

	// When set to true, configure to create ssh key in the `/tmp` directory instead of current directory in the machine kicking off the BOSH environment
	// provisioning the BOSH director VM. This field doesn't control the Cloud Foundry deployments that the BOSH CPI server starts up in BOSH director VM.
	UseTmpDirForSSHKey bool `json:"useTmpDirForSSHKey,omitempty"`

	// (For testing only) If set to true, `ProxyCommand=none` will be set in SSH commands, like `scp`, `ssh`, to disable proxy server for establishing the connection.
	// The flag is used for testing from Google machines like cloudtop with specific ssh config setting with `Proxycommand corp-ssh-helper %h %p`.
	DisableSSHProxy bool `json:"disableSSHProxy,omitempty"`

	// If set to true, metadata server will be used to store the BOSH Agent settings.
	// Else, local file system will be used
	UseMetadataServer bool `json:"useMetadataServer,omitempty"`

	// Timeout configuration in minutes for the waiting logic in BOSH CPI when interacting with GDCH infrastructure resources. When empty, default timeout configuration will be used.
	Timeout WaitTimeout `json:"timeout,omitempty"`

	// Zone where the BOSH director VM is provisioned in on multi-zone environment, when specified, zonalAPIs map must be non-empty
	LeaderZone string `json:"leaderZone,omitempty"`

	// To be used only for testing
	// If not provided, stemcell integration test is skipped
	// and VM integration test uses GDCH golden images
	StemcellInfo StemcellInfo `json:"stemcellInfo,omitempty"`

	// [Deprecated] Indicates if the underlying GDCH deployment is multizone.
	// This flag is deprecated and retained only for backward compatibility;
	// the CPI always operates in multizone mode.
	IsMultizone bool `json:"isMultiZone,omitempty"`

	// If true, enables "hot" attachment and detachment of persistent disks to a running VM.
	// Else (default), "cold" attachment used instead (requires a VM reboot).
	UseDiskHotplug bool `json:"useDiskHotplug,omitempty"`

	// Use CloudNAT Gateways for Egress.
	UseCloudNATGateways bool `json:"useCloudNATGateways,omitempty"`

	//Use RefreshImageImport to force custom image import from scratch
	RefreshImageImport bool `json:"refreshImageImport,omitempty"`

	// Use Tier 1 Network. VMs with Tier 1 networking configurations are especially useful
	// for large distributed compute workloads with lots of heavy internode communications.
	UseTier1Network bool `json:"useTier1Network,omitempty"`
}

// Config of GDCH service identity private key
type ServiceAccountKeyConfig struct {
	//Service account name. It will be used as the `iss` and `sub` claim in the self signed JWT.
	ServiceAccountName string `json:"serviceAccountName"`

	// Project ID associated with the service account credential.
	Project string `json:"project"`

	// The OAuth 2.0 Token URI.
	TokenURI string `json:"tokenUri"`

	// ID of private key
	PrivateKeyID string `json:"privateKeyID"`

	// Private key plain text content
	PrivateKey string `json:"privateKey"`
}

// Organization API configuration mapping zone to associated API endpoint
type OrgAPIConfig struct {
	// Global API endpoint, mapped via a reserved key "global"
	GlobalAPI string `json:"globalAPI,omitempty"`
	// Zonal API endpoints configuration, mapping GDCH zone (key) to associated zonal API endpoint (value)
	ZonalAPIs map[string]string `json:"zonalAPIs,omitempty"`
}

// Server port running on GDCM VM instance
type ServerPort struct {
	// Name of the port
	Name string `json:"name"`
	// Type of the port, e.g. "TCP"
	Protocol string `json:"protocol"`
	// Port number
	Port int32 `json:"port"`
}

// S3 credential for GDCH object storage
type S3Credential struct {
	// Base64 encoded access key ID
	AccessKeyID string `json:"accessKeyID"`
	// Base64 encoded access key
	AccessKey string `json:"accessKey"`
}

// Timeout configuration in minutes
type WaitTimeout struct {
	DiskCreation        int32 `json:"diskCreation"`
	DiskDeletion        int32 `json:"diskDeletion"`
	HotDiskAttachment   int32 `json:"hotDiskAttachment"`
	HotDiskDetachment   int32 `json:"hotDiskDetachment"`
	DiskResize          int32 `json:"diskResize"`
	SnapshotCreation    int32 `json:"snapshotCreation"`
	SnapshotDeletion    int32 `json:"snapshotDeletion"`
	StemcellCreation    int32 `json:"stemcellCreation"`
	StemcellDeletion    int32 `json:"stemcellDeletion"`
	StemcellReplication int32 `json:"stemcellReplication"`
	VMDeletion          int32 `json:"vmDeletion"`
	VMStart             int32 `json:"vmStart"`
	VMStop              int32 `json:"vmStop"`
}

// Stemcell info used in testing
type StemcellInfo struct {
	ImagePath    string `json:"imagePath,omitempty"`
	ManifestPath string `json:"manifestPath,omitempty"`
}

func (c *Config) Validate() error {
	if len(c.GDC.Org) == 0 {
		return errors.New("must provide non-empty Org")
	}

	if len(c.GDC.LeaderZone) > 0 {
		if len(c.GDC.OrgAPIEndpoints.ZonalAPIs) == 0 {
			return errors.New("must provide non-empty zonalAPIs map when leaderZone is specified")
		}
		if len(c.GDC.OrgAPIEndpoints.ZonalAPIs[c.GDC.LeaderZone]) == 0 {
			return fmt.Errorf("must provide non-empty config for zone %q in zonalAPIs map", c.GDC.LeaderZone)
		}
	} else if len(c.GDC.OrgAdminURL) == 0 {
		return errors.New("must provide non-empty OrgAdminURL")
	}

	if len(c.GDC.ServiceAccountKey.ServiceAccountName) == 0 {
		return errors.New("must provide non-empty ServiceAccountName")
	}

	if len(c.GDC.ServiceAccountKey.Project) == 0 {
		return errors.New("must provide non-empty Project")
	}

	if len(c.GDC.ServiceAccountKey.TokenURI) == 0 {
		return errors.New("must provide non-empty TokenURI")
	}

	if len(c.GDC.ServiceAccountKey.PrivateKeyID) == 0 {
		return errors.New("must provide non-empty PrivateKeyID")
	}

	if len(c.GDC.ServiceAccountKey.PrivateKey) == 0 {
		return errors.New("must provide non-empty PrivateKey")
	}

	if len(strings.TrimSpace(c.GDC.CAData)) == 0 {
		return errors.New("must provide non-empty CAData")
	}

	if len(strings.TrimSpace(c.GDC.StorageCredential.AccessKeyID)) == 0 {
		return errors.New("must provide non-empty StorageCredential.AccessKeyID")
	}

	if len(strings.TrimSpace(c.GDC.StorageCredential.AccessKey)) == 0 {
		return errors.New("must provide non-empty StorageCredential.AccessKey")
	}

	if err := c.Agent.Validate(); err != nil {
		return fmt.Errorf("validating Agent configuration: %w", err)
	}

	return nil
}

// Sets the default values for optional fields.
func setDefaultValues(c *Config) {
	if len(c.GDC.ImageProject) == 0 {
		// If unspecified, use the same project from ServiceAccountKey for
		// storing/retrieving Stemcells.
		c.GDC.ImageProject = c.GDC.ServiceAccountKey.Project
	}

	// Set the leader zone's management-plane API endpoint for multi-zone environment when leader zone and associated configs are specified
	if len(c.GDC.LeaderZone) > 0 {
		c.GDC.OrgAdminURL = c.GDC.OrgAPIEndpoints.ZonalAPIs[c.GDC.LeaderZone]
	}

	// Set to default timeout in minutes when empty
	if c.GDC.Timeout.DiskCreation == 0 {
		c.GDC.Timeout.DiskCreation = 45
	}
	if c.GDC.Timeout.DiskDeletion == 0 {
		c.GDC.Timeout.DiskDeletion = 2
	}
	if c.GDC.Timeout.HotDiskAttachment == 0 {
		c.GDC.Timeout.HotDiskAttachment = 5
	}
	if c.GDC.Timeout.HotDiskDetachment == 0 {
		c.GDC.Timeout.HotDiskDetachment = 5
	}
	if c.GDC.Timeout.DiskResize == 0 {
		c.GDC.Timeout.DiskResize = 5
	}
	if c.GDC.Timeout.SnapshotCreation == 0 {
		c.GDC.Timeout.SnapshotCreation = 5
	}
	if c.GDC.Timeout.SnapshotDeletion == 0 {
		c.GDC.Timeout.SnapshotDeletion = 5
	}
	if c.GDC.Timeout.StemcellCreation == 0 {
		// Importing image takes a long time, on average it's ~40 mins, sometimes can reach hours
		c.GDC.Timeout.StemcellCreation = 150
	}
	if c.GDC.Timeout.StemcellDeletion == 0 {
		c.GDC.Timeout.StemcellDeletion = 5
	}
	if c.GDC.Timeout.StemcellReplication == 0 {
		c.GDC.Timeout.StemcellReplication = 2
	}

	if c.GDC.Timeout.VMDeletion == 0 {
		c.GDC.Timeout.VMDeletion = 2
	}
	if c.GDC.Timeout.VMStart == 0 {
		c.GDC.Timeout.VMStart = 15
	}
	if c.GDC.Timeout.VMStop == 0 {
		c.GDC.Timeout.VMStop = 5
	}
}

// NewConfigFromPath returns a new Config from a file path.
func NewConfigFromPath(path string, fs boshsys.FileSystem) (*Config, error) {
	config := &Config{}

	bytes, err := fs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config '%s': %w", path, err)
	}

	if err = json.Unmarshal(bytes, config); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	if err = config.Validate(); err != nil {
		return nil, fmt.Errorf("validating configuration: %w", err)
	}

	setDefaultValues(config)

	return config, nil
}

// Copyright 2026 Google LLC
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

package consts

const (
	// CPILabel is the label key is used to indicate the image import is done by BOSH CPI
	CPILabel = "vmimage.cfbosh.io/provisioner"
	// VMExternalEgressControlLabelKey is the label key used to control external egress for a VM
	VMExternalEgressControlLabelKey = "egress.networking.gke.io/enabled"
	// VMBackupPlanLabelKey is the label key used to indicate the disk name of the backup plan
	VMBackupPlanLabelKey = "vmbackupplan.cfbosh.io/disk-name"
	// ZoneKey is the label key used to indicate the zone of the infrastructure provisioned
	ZoneKey = "zone"
)

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

package service

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
)

func TestGetObjectZone(t *testing.T) {
	testCases := map[string]struct {
		zone1Objects []client.Object
		zone2Objects []client.Object
		error        bool
		expectedZone string
	}{
		"object not found": {
			error: true,
		},
		"object found in zone1": {
			zone1Objects: []client.Object{virtualMachineDisk("disk-id")},
			error:        false,
			expectedZone: "zone1",
		},
		"object found in zone2": {
			zone2Objects: []client.Object{virtualMachineDisk("disk-id")},
			error:        false,
			expectedZone: "zone2",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			zone1Client := test.FakeClient(tc.zone1Objects...)
			zone2Client := test.FakeClient(tc.zone2Objects...)
			cloudClients := &kubeclient.KubeClients{
				ZonalAPIServers: map[string]client.WithWatch{
					"zone1": zone1Client,
					"zone2": zone2Client,
				},
			}
			multizoneFinder := NewMultizoneFinder(cloudClients, "project-id")
			zone, err := multizoneFinder.GetZone(context.Background(), "disk-id", &vmv1.VirtualMachineDisk{})
			if tc.error && err == nil {
				t.Fatal("got nil but expected an error")
			}
			if !tc.error && err != nil {
				t.Fatalf("got unexpected error: %v", err)
			}
			if !tc.error && zone != tc.expectedZone {
				t.Fatalf("expected to find the object in zone %s but found in zone %s", tc.expectedZone, zone)
			}
		})
	}
}

func virtualMachineDisk(name string) *vmv1.VirtualMachineDisk {
	return &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "project-id",
		},
	}
}

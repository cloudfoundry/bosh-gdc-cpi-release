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

package metadata

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	config "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
)

func TestUpdate_Fail(t *testing.T) {
	testVMID := "test-vm-id"
	testAgentEnv := getAgentEnv()

	tests := []struct {
		name           string
		vmID           string
		agentEnv       apiv1.AgentEnv
		metadataClient *MetadataClient
		errStr         string
	}{
		{
			name:           "empty vmID",
			vmID:           "",
			agentEnv:       testAgentEnv,
			metadataClient: GetMockMetadataClient(http.StatusOK, "{\"status\": \"success\"}"),
			errStr:         "vmID not provided",
		},
		{
			name:           "server failure",
			vmID:           testVMID,
			agentEnv:       testAgentEnv,
			metadataClient: GetMockMetadataClient(http.StatusInternalServerError, "{\"status\": \"success\"}"),
			errStr:         fmt.Sprintf("failed with status: %d", http.StatusInternalServerError),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.metadataClient
			err := m.Update(context.Background(), tt.vmID, tt.agentEnv)

			if err == nil {
				t.Errorf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errStr) {
				t.Errorf("MetadataClient.Update() error = %v, wantErrStr %v", err, tt.errStr)
			}
		})
	}
}

func TestUpdate_Success(t *testing.T) {
	vmID := "test-vm-id"
	agentEnv := getAgentEnv()
	metadataClient := GetMockMetadataClient(http.StatusOK, "{\"status\": \"success\"}")

	if err := metadataClient.Update(context.Background(), vmID, agentEnv); err != nil {
		t.Fatalf("got error: %v", err)
	}
}

func TestFetch_Fail(t *testing.T) {
	testVMID := "test-vm-id"

	tests := []struct {
		name           string
		vmID           string
		metadataClient *MetadataClient
		errStr         string
	}{
		{
			name:           "empty vmID",
			vmID:           "",
			metadataClient: GetMockMetadataClient(http.StatusOK, "{\"status\": \"success\"}"),
			errStr:         "vmID not provided",
		},
		{
			name:           "server failure",
			vmID:           testVMID,
			metadataClient: GetMockMetadataClient(http.StatusInternalServerError, "{\"status\": \"success\"}"),
			errStr:         fmt.Sprintf("failed with status: %d", http.StatusInternalServerError),
		},
		{
			name:           "empty response body",
			vmID:           testVMID,
			metadataClient: GetMockMetadataClient(http.StatusOK, ""),
			errStr:         "empty response from server",
		},
		{
			name:           "empty bosh settings in response",
			vmID:           testVMID,
			metadataClient: GetMockMetadataClient(http.StatusOK, "{\"status\": \"success\"}"),
			errStr:         "missing bosh settings value in response",
		},
		{
			name:           "invalid bosh settings",
			vmID:           testVMID,
			metadataClient: GetMockMetadataClient(http.StatusOK, "{\"metadata\": {\"items\": [{\"key\": \"bosh-settings\", \"value\": \"empty\"}]}}\n"),
			errStr:         "unmarshal agent env from extracted value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.metadataClient
			_, err := m.Fetch(context.Background(), tt.vmID)

			if err == nil {
				t.Errorf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.errStr) {
				t.Errorf("MetadataClient.Fetch() error = %v, wantErrStr %v", err, tt.errStr)
			}
		})
	}
}

func TestFetch_Success(t *testing.T) {
	testVMID := "test-vm-id"
	agentEnv := getAgentEnv()
	agentEnvBytes, _ := agentEnv.AsBytes()
	escapedAgentEnv := strings.ReplaceAll(string(agentEnvBytes), `"`, `\"`)
	metadataClient := GetMockMetadataClient(http.StatusOK, fmt.Sprintf("{\"metadata\": {\"items\": [{\"key\": \"bosh-settings\", \"value\": \"%s\"}]}}\n", escapedAgentEnv))

	actualAgentEnv, err := metadataClient.Fetch(context.Background(), testVMID)
	if err != nil {
		t.Fatalf("got error: %v", err)
	}

	actualJson, err := test.ConvertAgentEnvToJsonMap(actualAgentEnv)
	if err != nil {
		t.Fatalf("convert actual agent env to json: %v", err)
	}

	expectedJson, err := test.ConvertAgentEnvToJsonMap(agentEnv)
	if err != nil {
		t.Fatalf("convert expected agent env to bytes: %v", err)
	}

	if diff := cmp.Diff(expectedJson, actualJson); diff != "" {
		t.Fatalf("unexpected agent env; diff (-want +got):\n%s", diff)
	}
}

func TestGetOrgSuffix_Success(t *testing.T) {
	testCases := []struct {
		name      string
		gdcConfig *config.GDCConfig
		zone      []string
		want      string
	}{
		{
			name: "no zone arg uses org admin url api endpoint",
			gdcConfig: &config.GDCConfig{
				OrgAdminURL: "zone1.org-1.google.gdch.test",
				Org:         "org-1",
			},
			zone: []string{},
			want: "org-1.google.gdch.test",
		},
		{
			name: "zone arg uses zonalapis endpoint",
			gdcConfig: &config.GDCConfig{
				OrgAPIEndpoints: config.OrgAPIConfig{
					ZonalAPIs: map[string]string{
						"us-east-1a": "https://xxx-kube.org-name.us-east-1a.xxx.xxx",
						"us-west-1b": "https://xxx-kube.org-name.us-west-1b.xxx.xxx",
					},
				},
				Org: "org-name",
			},
			zone: []string{"us-east-1a"},
			want: "org-name.us-east-1a.xxx.xxx",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := getOrgSuffix(tc.gdcConfig, tc.zone...)
			if err != nil {
				t.Fatalf("got error %q, but expected success", err)
			}
			if result != tc.want {
				t.Errorf("got %q, want %q", result, tc.want)
			}
		})
	}
}

func TestGetOrgSuffix_Failed(t *testing.T) {
	testCases := []struct {
		name      string
		gdcConfig *config.GDCConfig
		zone      []string
		wantErr   string
	}{
		{
			name: "single zone org name not found in org admin url endpoint",
			gdcConfig: &config.GDCConfig{
				OrgAdminURL: "zone1.org-1.google.gdch.test",
				Org:         "org-2",
			},
			zone:    []string{},
			wantErr: "\"org-2\" not found",
		},
		{
			name: "multizone org name not found in zonal api endpoint",
			gdcConfig: &config.GDCConfig{
				OrgAPIEndpoints: config.OrgAPIConfig{
					ZonalAPIs: map[string]string{
						"us-east-1a": "https://xxx-kube.org-1.us-east-1a.xxx.xxx",
					},
				},
				Org: "org-2",
			},
			zone:    []string{"us-east-1a"},
			wantErr: "\"org-2\" not found",
		},
		{
			name: "provided zone not in zonal apis",
			gdcConfig: &config.GDCConfig{
				OrgAPIEndpoints: config.OrgAPIConfig{
					ZonalAPIs: map[string]string{
						"us-east1-a": "https://xxx-kube.org-1.us-east-1a.xxx.xxx",
					},
				},
				Org: "org-1",
			},
			zone:    []string{"europe-central-1"},
			wantErr: "zone \"europe-central-1\" missing",
		},
		{
			name: "provided zone has empty api endpoint",
			gdcConfig: &config.GDCConfig{
				OrgAPIEndpoints: config.OrgAPIConfig{
					ZonalAPIs: map[string]string{
						"us-west-1a": "",
					},
				},
				Org: "org-1",
			},
			zone:    []string{"us-west-1a"},
			wantErr: "endpoint is empty",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, gotErr := getOrgSuffix(tc.gdcConfig, tc.zone...)
			if gotErr == nil {
				t.Fatalf("expected error but got nil")
			}
			if !strings.Contains(gotErr.Error(), tc.wantErr) {
				t.Errorf("got error %q, want it to contain %q", gotErr, tc.wantErr)
			}
		})
	}
}

func GetMockMetadataClient(mockStatusCode int, mockResponse string) *MetadataClient {
	mockClient := NewMockHTTPClient(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: mockStatusCode,
			Body:       io.NopCloser(bytes.NewBufferString(mockResponse)),
			Header:     http.Header{"Content-Type": {"application/json"}},
		}, nil
	})

	return &MetadataClient{
		ProjectID: "123",
		OrgSuffix: "test-org",
		Client:    &mockClient.Client,
	}
}

type RoundTripFunc func(req *http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type MockHTTPClient struct {
	http.Client
}

func NewMockHTTPClient(rtf RoundTripFunc) *MockHTTPClient {
	if rtf == nil {
		return &MockHTTPClient{Client: http.Client{Transport: http.DefaultTransport}}
	}
	return &MockHTTPClient{
		Client: http.Client{Transport: rtf},
	}
}

func getAgentEnv() apiv1.AgentEnv {
	env := apiv1.NewVMEnv(map[string]interface{}{"fake-env-key": "fake-env-value"})
	agentOptions := apiv1.AgentOptions{
		Mbus: "https://user:password@127.0.0.1:4321/agent",
		NTP:  []string{"fake-ntp"},
	}

	return apiv1.NewAgentEnvFactory().ForVM(
		apiv1.NewAgentID("fake-agent-id"), apiv1.NewVMCID("fake-vm-id"), getNetworks(), env, agentOptions)
}

func getNetworks() apiv1.Networks {
	net1 := apiv1.NewNetwork(apiv1.NetworkOpts{
		Type:    "fake-type",
		IP:      "fake-ip",
		Netmask: "fake-netmask",
		Gateway: "fake-gateway",
		DNS:     []string{"fake-dns"},
		Default: []string{"fake-default"},
	})

	net2 := apiv1.NewNetwork(apiv1.NetworkOpts{
		Type:    "another-fake-type",
		IP:      "another-fake-ip",
		Netmask: "another-fake-netmask",
		Gateway: "another-fake-gateway",
		DNS:     []string{"another-fake-dns"},
		Default: []string{},
		Alias:   "fake-iface-name",
		Routes: []apiv1.Route{
			{
				Destination: "10.0.0.0",
				Netmask:     "255.255.0.0",
				Gateway:     "another-fake-gateway",
			},
		},
	})

	return apiv1.Networks{
		"fake-net-name":         net1,
		"another-fake-net-name": net2,
	}
}

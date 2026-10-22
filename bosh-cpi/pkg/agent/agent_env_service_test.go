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

package agent_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"github.com/google/go-cmp/cmp"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/agent"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
)

const (
	vmName = "test-vm"
)

func TestUpdate_InvalidVMName(t *testing.T) {
	s := agent.AgentEnvService{
		SSHManager: &ssh{},
	}

	ctx := context.Background()
	if err := s.Update(ctx, "", "1.2.3.4", getAgentEnv()); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdate_InvalidIngressIP(t *testing.T) {
	s := agent.AgentEnvService{
		SSHManager: &ssh{},
	}

	ctx := context.Background()
	if err := s.Update(ctx, vmName, "", getAgentEnv()); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetch_InvalidVMName(t *testing.T) {
	s := agent.AgentEnvService{
		SSHManager: &ssh{},
	}

	ctx := context.Background()
	if _, err := s.Fetch(ctx, "", "1.2.3.4"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetch_InvalidIngressIP(t *testing.T) {
	s := agent.AgentEnvService{
		SSHManager: &ssh{},
	}

	ctx := context.Background()
	if _, err := s.Fetch(ctx, vmName, ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestUpdateSuccess(t *testing.T) {
	vmIngressIP := "1.2.3.4"
	agentEnv := getAgentEnv()

	s := agent.AgentEnvService{
		SSHManager: &ssh{
			expectedName: vmName,
			expectedEnv:  agentEnv,
			expectedIP:   vmIngressIP,
		},
	}

	ctx := context.Background()
	if err := s.Update(ctx, vmName, vmIngressIP, agentEnv); err != nil {
		t.Fatalf("got error: %v", err)
	}
}

func TestFetchSuccess(t *testing.T) {
	vmIngressIP := "1.2.3.4"
	agentEnv := getAgentEnv()
	agentEnvBytes, err := agentEnv.AsBytes()
	if err != nil {
		t.Fatalf("convert expected agent env to bytes: %v", err)
	}

	testCases := map[string]struct {
		downloadBytes []byte
		expectedEnv   apiv1.AgentEnv
	}{
		"agentEnv exists on the target VM":         {downloadBytes: agentEnvBytes, expectedEnv: agentEnv},
		"agentEnv does not exist on the target VM": {downloadBytes: nil, expectedEnv: &apiv1.AgentEnvImpl{}},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			s := agent.AgentEnvService{
				SSHManager: &ssh{
					downloadBytes: tc.downloadBytes,
				},
			}

			ctx := context.Background()
			actualAgentEnv, err := s.Fetch(ctx, vmName, vmIngressIP)
			if err != nil {
				t.Fatalf("fetch: %v", err)
			}

			actualJson, err := test.ConvertAgentEnvToJsonMap(actualAgentEnv)
			if err != nil {
				t.Fatalf("convert actual agent env to json: %v", err)
			}

			expectedJson, err := test.ConvertAgentEnvToJsonMap(tc.expectedEnv)
			if err != nil {
				t.Fatalf("convert expected agent env to bytes: %v", err)
			}

			if diff := cmp.Diff(expectedJson, actualJson); diff != "" {
				t.Fatalf("unexpected agent env; diff (-want +got):\n%s", diff)
			}
		})
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

type ssh struct {
	expectedName string
	expectedEnv  apiv1.AgentEnv
	expectedIP   string

	downloadBytes []byte
}

func (s *ssh) Upload(ctx context.Context, name, ip, src, dest string) error {
	if s.expectedName != name {
		return fmt.Errorf("unexpected vm name: got %q but want %q", name, s.expectedName)
	}

	if s.expectedIP != ip {
		return fmt.Errorf("unexpected ip: got %q but want %q", ip, s.expectedIP)
	}

	bs, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read src file %q: %v", src, err)
	}

	expectedBS, err := s.expectedEnv.AsBytes()
	if err != nil {
		return fmt.Errorf("convert agent env to bytes: %w", err)
	}

	if diff := cmp.Diff(expectedBS, bs); diff != "" {
		return fmt.Errorf("unexpected agent env; diff (-want +got):\n%s", diff)
	}

	return nil
}

func (s *ssh) Download(ctx context.Context, vmName, vmIP, targetFile string) ([]byte, error) {
	return s.downloadBytes, nil
}

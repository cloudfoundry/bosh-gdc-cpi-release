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

package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
)

const (
	boshSettingsPath = "/home/bosh-ssh-user/bosh-settings.json"
)

type sshManager interface {
	Upload(ctx context.Context, vmName, vmIP string, sourceFile string, targetFile string) error
	Download(ctx context.Context, vmName, vmIP, targetFile string) ([]byte, error)
}

type AgentEnvService struct {
	SSHManager sshManager
}

type AgentEnvServices struct {
	ZonalAgentEnvServices map[string]AgentEnvService
}

func (s *AgentEnvService) Fetch(ctx context.Context, vmName, vmIngressIP string) (apiv1.AgentEnv, error) {
	if len(vmName) == 0 {
		return nil, errors.New("vm name is not provided")
	}
	if len(vmIngressIP) == 0 {
		return nil, errors.New("vm ingress ip is not provided")
	}

	bytes, err := s.SSHManager.Download(ctx, vmName, vmIngressIP, boshSettingsPath)
	if err != nil {
		return nil, fmt.Errorf("download env from vm: %w", err)
	}

	if len(bytes) == 0 {
		return &apiv1.AgentEnvImpl{}, nil
	}

	agentEnv, err := apiv1.AgentEnvFactory{}.FromBytes(bytes)
	if err != nil {
		return nil, fmt.Errorf("unmarshall agent env: %w", err)
	}

	return agentEnv, nil
}

func (s *AgentEnvService) Update(ctx context.Context, vmName, vmIngressIP string, agentEnv apiv1.AgentEnv) error {
	if len(vmName) == 0 {
		return errors.New("set agent setting, vm name is not provided")
	}

	if len(vmIngressIP) == 0 {
		return errors.New("set agent setting, vm ingress IP is not provided")
	}

	bytes, err := agentEnv.AsBytes()
	if err != nil {
		return fmt.Errorf("marshall agent env %v: %w", agentEnv, err)
	}

	file, err := os.CreateTemp(os.TempDir(), "bosh-setting-*.json")
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}

	if err := writeFileAndPermissions(file.Name(), bytes, 0644); err != nil {
		return fmt.Errorf("write file %s: %w", file.Name(), err)
	}

	if err := s.SSHManager.Upload(ctx, vmName, vmIngressIP, file.Name(), boshSettingsPath); err != nil {
		return fmt.Errorf("upload agent setting file: %w", err)
	}

	return nil
}

func writeFileAndPermissions(name string, data []byte, perm fs.FileMode) error {
	stat, err := os.Stat(name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// when the file already exists, set desired permissions
	if stat != nil && stat.Mode() != perm {
		if err := os.Chmod(name, perm); err != nil {
			return err
		}
	}

	// suppress go/nogo-check#disallowedfunction os.WriteFile
	if err := os.WriteFile(name, data, perm); err != nil {
		return err
	}
	return nil
}

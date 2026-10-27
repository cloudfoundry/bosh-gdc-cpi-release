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

// Package ssh handles ssh/scp interactions with GDCH VMs.
package ssh

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	vmservice "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/vm"
)

const (
	// The default user for ssh/scp.
	DefaultSSHUser = "bosh-ssh-user"

	// BOSH's working directory path in BOSH managed VMs, https://bosh.io/docs/vm-struct/
	defaultBoshDir = "/var/vcap"
	defaultTempDir = "/tmp"
	// The default file name for the ssh private key.
	defaultKeyFileName = "bosh-gdch-vm-access"

	sshPort = "22"
)

var (
	waitCMDSuccess = wait.Waiting{
		Wait:    5 * time.Minute,
		Every:   3 * time.Second,
		Timeout: 5 * time.Minute,
	}

	waitSSHConnectionSuccess = wait.Waiting{
		Wait:    15 * time.Minute,
		Every:   15 * time.Second,
		Timeout: 15 * time.Minute,
	}
)

type vmChecker interface {
	EnsureVMExistAndRunning(vmName string) error
}

type commandProvider interface {
	Command(name string, arg ...string) *exec.Cmd
}

type execCommandProvider struct{}

func (*execCommandProvider) Command(name string, arg ...string) *exec.Cmd {
	return exec.Command(name, arg...)
}

type fileSystemProvider interface {
	Stat(fileName string) (os.FileInfo, error)
}

type execFileSystemProvider struct{}

func (*execFileSystemProvider) Stat(fileName string) (os.FileInfo, error) {
	return os.Stat(fileName)
}

type dialer interface {
	ensureSSHConnection(ctx context.Context, host string, port string, timeout wait.Waiting) error
}

type connChecker struct {
	waiter wait.Waiter
}

func (c *connChecker) ensureSSHConnection(ctx context.Context, host string, port string, timeout wait.Waiting) error {
	if err := c.waiter.WaitForSuccess(ctx, "wait until ssh connection succeeds", timeout, func(ctx context.Context) error {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 10*time.Second)
		if err != nil {
			return fmt.Errorf("check ssh connection: %v", err)
		}
		conn.Close()

		return nil
	}); err != nil {
		return fmt.Errorf("timeout awaiting ssh connection: %v", err)
	}

	return nil
}

// Initialize the ssh/scp settings and returns a ssh/scp manager.
func Initialize(disableProxy, useTmpDir bool, cloudClient client.Client,
	projectID string, waiter wait.Waiter, vmarService vmservice.VMARAccessChecker) (*Manager, error) {
	cmdProvider := &execCommandProvider{}
	fsProvider := &execFileSystemProvider{}
	connChecker := &connChecker{waiter}
	fileName, err := sshKeyPath(defaultKeyFileName, cmdProvider, fsProvider, useTmpDir)
	if err != nil {
		return nil, fmt.Errorf("set up SSH key: %w", err)
	}

	publicKeyFile := fmt.Sprintf("%s.pub", fileName)
	bs, err := os.ReadFile(publicKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read public key file %q: %w", publicKeyFile, err)
	}
	pk := string(bs)

	return &Manager{
		keyPath:      fileName,
		disableProxy: disableProxy,
		vmService: &vmservice.VMChecker{
			CloudClient: cloudClient,
			ProjectID:   projectID,
		},
		vmarService: vmarService,
		cmdProvider: cmdProvider,
		connChecker: connChecker,
		waiter:      waiter,
		publicKey:   pk,
	}, nil
}

// Manager manages ssh/scp settings and connections.
type Manager struct {
	keyPath      string
	disableProxy bool
	vmService    vmChecker
	vmarService  vmservice.VMARAccessChecker
	cmdProvider  commandProvider
	connChecker  dialer
	waiter       wait.Waiter
	publicKey    string
}

// Upload copies a local file to a VM instance over scp.
func (m *Manager) Upload(ctx context.Context, vmName, vmIP string, sourceFile string, targetFile string) error {
	if err := m.vmService.EnsureVMExistAndRunning(vmName); err != nil {
		return fmt.Errorf("check state of vm %s: %w", vmName, err)
	}

	if err := m.vmarService.EnsureVMARExistAndReady(vmName, m.publicKey); err != nil {
		return fmt.Errorf("check access request for vm %s: %w", vmName, err)
	}

	if err := m.connChecker.ensureSSHConnection(ctx, vmIP, sshPort, waitSSHConnectionSuccess); err != nil {
		return fmt.Errorf("ssh connection to VM %q was not successful: %w", vmName, err)
	}

	target := fmt.Sprintf("%s@%s:%s", DefaultSSHUser, vmIP, targetFile)

	var args []string
	if m.disableProxy {
		args = append(args, "-o", "ProxyCommand=none")
	}
	args = append(args, "-o", "StrictHostKeyChecking=no")
	args = append(args, "-i", m.keyPath, sourceFile, target)

	var out []byte
	var cmdErr error
	msg := "upload env setting"

	// Wait till command success.
	// VM reboots may affect vm to accept traffic even when vm is restarted and running.
	// Wait and retry till timeout or vm can accept the connection.
	if err := m.waiter.WaitForSuccess(ctx, msg, waitCMDSuccess, func(ctx context.Context) error {
		out, cmdErr = m.cmdProvider.Command("scp", args...).CombinedOutput()
		return cmdErr
	}); err != nil {
		return fmt.Errorf("scp (%+v): %w (%s)", args, err, out)
	}

	return nil
}

// Download copies a remote file to a local byte slice over scp.
func (m *Manager) Download(ctx context.Context, vmName, vmIP, sourceFile string) ([]byte, error) {
	if err := m.vmarService.EnsureVMARExistAndReady(vmName, m.publicKey); err != nil {
		return nil, fmt.Errorf("check access request for vm %s: %w", vmName, err)
	}

	if err := m.connChecker.ensureSSHConnection(ctx, vmIP, sshPort, waitSSHConnectionSuccess); err != nil {
		return nil, fmt.Errorf("ssh connection to VM %q was not successful: %w", vmName, err)
	}

	var args []string
	if m.disableProxy {
		args = append(args, "-o", "ProxyCommand=none")
	}
	args = append(args, "-o", "StrictHostKeyChecking=no")

	target := fmt.Sprintf("%s@%s", DefaultSSHUser, vmIP)
	args = append(args, "-q", "-i", m.keyPath, target, "cat", sourceFile)

	var out []byte
	var cmdErr error
	msg := "download env setting"

	// Wait till command success.
	// VM reboots may affect vm to accept traffic even when vm is restarted and running.
	// Wait and retry till timeout or vm can accept the connection.
	if err := m.waiter.WaitForSuccess(ctx, msg, waitCMDSuccess, func(ctx context.Context) error {
		out, cmdErr = m.cmdProvider.Command("ssh", args...).CombinedOutput()
		return cmdErr
	}); err != nil {
		return nil, fmt.Errorf("ssh (%+v): %w (%s)", args, err, out)
	}

	return out, nil
}

func sshKeyPath(keyFileName string, cmdProvider commandProvider, fsProvider fileSystemProvider, useTmpDir bool) (string, error) {
	sshKeyPath, err := filepath.Abs(keyFileName)
	if err != nil {
		return "", fmt.Errorf("get absolute path for %q: %w", keyFileName, err)
	}

	// As of BOSH 283.0.0, the director workers run inside a BPM container where /home is not mounted.
	// When initializing CPI from the BOSH director VM, write the SSH key to os.TempDir() which maps
	// to the BPM writable surface (/var/vcap/data/director/tmp) to avoid permission errors.
	if strings.HasPrefix(sshKeyPath, defaultBoshDir) {
		sshKeyPath = filepath.Join(os.TempDir(), filepath.Base(keyFileName))
	} else if useTmpDir {
		// Only control the location of created ssh key files for the BOSH deployment of creating the BOSH environment that the CPI server
		// starts up in the user's working machine
		sshKeyPath = fmt.Sprintf("%s/%s", defaultTempDir, filepath.Base(keyFileName))
	}

	if _, err := fsProvider.Stat(sshKeyPath); err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("check ssh key file %q: %w", keyFileName, err)
		}
		if out, err := cmdProvider.Command("ssh-keygen", "-t", "rsa", "-b", "4096", "-C", DefaultSSHUser, "-f", sshKeyPath, "-q", "-N", "").CombinedOutput(); err != nil {
			return "", fmt.Errorf("generate ssh key file %q: %w\n(%s)", sshKeyPath, err, out)
		}
	}
	return sshKeyPath, nil
}

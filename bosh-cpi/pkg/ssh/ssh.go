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
	"sync"
	"syscall"
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
	// Suffix of the sidecar file used to serialize ssh key generation.
	lockFileSuffix = ".lock"
	// Suffix ssh-keygen gives the public half of the pair.
	pubKeyFileSuffix = ".pub"

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
	Remove(fileName string) error
	// Lock takes an exclusive advisory lock guarding fileName against other
	// processes and returns the function that releases it.
	Lock(fileName string) (unlock func(), err error)
}

type execFileSystemProvider struct{}

func (*execFileSystemProvider) Stat(fileName string) (os.FileInfo, error) {
	return os.Stat(fileName)
}

func (*execFileSystemProvider) Remove(fileName string) error {
	return os.Remove(fileName)
}

func (*execFileSystemProvider) Lock(fileName string) (func(), error) {
	// flock lives on the open file description, so separate opens contend even
	// within one process, and the kernel drops the lock when the holder exits.
	// A CPI killed mid-generation therefore cannot wedge later invocations.
	lockFile, err := os.OpenFile(fileName+lockFileSuffix, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file for %q: %w", fileName, err)
	}

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("lock %q: %w", lockFile.Name(), err)
	}

	return func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}, nil
}

// LazyManager defers Initialize until an operation actually needs SSH.
//
// bosh-cpi-go constructs the entire CPI before it looks at which method was
// requested, so eager setup made every call pay for SSH, including stemcell,
// disk and info calls that never reach a VM. Setup generates an RSA key pair
// and reads it back from disk, which turned unrelated operations into a
// failure surface: a clean-up task deleting stemcells could fail inside
// ssh-keygen, and the dispatcher reports any such failure as the thoroughly
// misleading "CPI must report api_version via info".
type LazyManager struct {
	initialize func() (*Manager, error)

	once    sync.Once
	manager *Manager
	err     error
}

func NewLazyManager(initialize func() (*Manager, error)) *LazyManager {
	return &LazyManager{initialize: initialize}
}

func (l *LazyManager) get() (*Manager, error) {
	l.once.Do(func() {
		l.manager, l.err = l.initialize()
	})

	return l.manager, l.err
}

func (l *LazyManager) Upload(ctx context.Context, vmName, vmIP string, sourceFile string, targetFile string) error {
	manager, err := l.get()
	if err != nil {
		return err
	}

	return manager.Upload(ctx, vmName, vmIP, sourceFile, targetFile)
}

func (l *LazyManager) Download(ctx context.Context, vmName, vmIP, sourceFile string) ([]byte, error) {
	manager, err := l.get()
	if err != nil {
		return nil, err
	}

	return manager.Download(ctx, vmName, vmIP, sourceFile)
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

	publicKeyFile := fileName + pubKeyFileSuffix
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

	out, err := m.runUntilSuccess(ctx, "upload env setting", "scp", args)
	if err != nil {
		return fmt.Errorf("scp (%+v): %w (%s)", args, err, out)
	}

	return nil
}

// runUntilSuccess retries name until it exits zero or the wait expires, and
// returns the combined output of the last attempt that produced any.
//
// The output is guarded by a mutex because the retrying goroutine can outlive
// WaitForSuccess: when the wait expires, an attempt may still be in flight and
// about to record its output, so reading it unsynchronized in the caller's
// error path is a data race.
func (m *Manager) runUntilSuccess(ctx context.Context, msg, name string, args []string) ([]byte, error) {
	var (
		mu  sync.Mutex
		out []byte
	)

	err := m.waiter.WaitForSuccess(ctx, msg, waitCMDSuccess, func(context.Context) error {
		// Wait till command success.
		// VM reboots may affect vm to accept traffic even when vm is restarted and running.
		// Wait and retry till timeout or vm can accept the connection.
		combined, cmdErr := m.cmdProvider.Command(name, args...).CombinedOutput()

		mu.Lock()
		defer mu.Unlock()
		out = combined

		return cmdErr
	})

	mu.Lock()
	defer mu.Unlock()

	return out, err
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

	out, err := m.runUntilSuccess(ctx, "download env setting", "ssh", args)
	if err != nil {
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

	// Once the pair exists it never changes, so the common case needs no locking
	// and leaves no lock file behind.
	complete, err := keyPairComplete(sshKeyPath, fsProvider)
	if err != nil {
		return "", err
	}
	if complete {
		return sshKeyPath, nil
	}

	// Creating it is another matter. The director runs several CPI processes at
	// once and they all share this path, so an existence check cannot stand on
	// its own: two processes can both find the key missing and both run
	// ssh-keygen. ssh-keygen refuses to overwrite an existing key and asks for
	// confirmation on stdin, which the CPI has already read to EOF, so the loser
	// of that race exits 1 and fails the whole CPI call. Holding a lock across
	// the re-check and the generation makes the loser wait and then observe a
	// complete key pair instead of racing to recreate it.
	unlock, err := fsProvider.Lock(sshKeyPath)
	if err != nil {
		return "", err
	}
	defer unlock()

	// Re-check under the lock, since we may have queued behind the process that
	// generated the pair.
	complete, err = keyPairComplete(sshKeyPath, fsProvider)
	if err != nil {
		return "", err
	}
	if complete {
		return sshKeyPath, nil
	}

	// Anything surviving to here is a partial pair left by a process that died
	// between ssh-keygen's two writes. It has to go: ssh-keygen would refuse to
	// overwrite the private key, and a private key with no public half looks
	// complete to the check above, so leaving it would wedge every later run.
	for _, path := range []string{sshKeyPath, sshKeyPath + pubKeyFileSuffix} {
		if err := fsProvider.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove incomplete ssh key file %q: %w", path, err)
		}
	}

	if out, err := cmdProvider.Command("ssh-keygen", "-t", "rsa", "-b", "4096", "-C", DefaultSSHUser, "-f", sshKeyPath, "-q", "-N", "").CombinedOutput(); err != nil {
		return "", fmt.Errorf("generate ssh key file %q: %w\n(%s)", sshKeyPath, err, out)
	}

	return sshKeyPath, nil
}

// keyPairComplete reports whether both halves of the key pair are on disk.
// A private key on its own is not enough: ssh-keygen writes it before the
// public key, and Initialize reads the public key immediately.
func keyPairComplete(sshKeyPath string, fsProvider fileSystemProvider) (bool, error) {
	for _, path := range []string{sshKeyPath, sshKeyPath + pubKeyFileSuffix} {
		if _, err := fsProvider.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}

			return false, fmt.Errorf("check ssh key file %q: %w", path, err)
		}
	}

	return true, nil
}

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

package ssh

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	vmservice "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/vm"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	"k8s.io/utils/clock"
)

const vmName = "test-vm-name"

var (
	mockCmdProvider = &statefulMockRunner{}
)

func init() {
	waitCMDSuccess = wait.Waiting{
		Wait:    3 * time.Second,
		Every:   1 * time.Second,
		Timeout: 3 * time.Second,
	}
}

func TestUpload_Success(t *testing.T) {
	mockCmdProvider = &statefulMockRunner{
		willSucceed: true,
	}
	ssh := getSSHManager(mockCmdProvider)

	if err := ssh.Upload(context.Background(), vmName, "1.2.3.4", "source.txt", "target.txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedSCPCommand := "scp -o ProxyCommand=none -o StrictHostKeyChecking=no -i /foo/bar/baz source.txt bosh-ssh-user@1.2.3.4:target.txt"
	if mockCmdProvider.ranCommand != expectedSCPCommand {
		t.Fatalf("expected command vs actual\n (%v)\n (%v)", expectedSCPCommand, mockCmdProvider.ranCommand)
	}
}

func TestUpload_Fail(t *testing.T) {
	mockCmdProvider = &statefulMockRunner{
		willSucceed: false,
	}
	ssh := getSSHManager(mockCmdProvider)

	if err := ssh.Upload(context.Background(), vmName, "1.2.3.4", "source.txt", "target.txt"); err == nil {
		t.Fatal("expected error, but none was given")
	}
}

func TestUpload_Fail_Connection_Timeout(t *testing.T) {
	mockCmdProvider = &statefulMockRunner{
		willSucceed: true,
	}
	ssh := getSSHManager(mockCmdProvider)
	ssh.connChecker = &mockConnChecker{
		err: errors.New("ssh connection timeout"),
	}

	if err := ssh.Upload(context.Background(), vmName, "1.2.3.4", "source.txt", "target.txt"); err != nil && !strings.Contains(err.Error(), "ssh connection timeout") {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func TestDownload_Success(t *testing.T) {
	mockCmdProvider = &statefulMockRunner{
		willSucceed: true,
	}
	ssh := getSSHManager(mockCmdProvider)

	if _, err := ssh.Download(context.Background(), vmName, "1.2.3.4", "foo.txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedSCPCommand := "ssh -o ProxyCommand=none -o StrictHostKeyChecking=no -q -i /foo/bar/baz bosh-ssh-user@1.2.3.4 cat foo.txt"
	if mockCmdProvider.ranCommand != expectedSCPCommand {
		t.Fatalf("expected command vs actual\n (%v)\n (%v)", expectedSCPCommand, mockCmdProvider.ranCommand)
	}
}

func TestSSHKeyPath_Succeed(t *testing.T) {
	keyPathInDirectorVM := filepath.Join(os.TempDir(), "ssh_key")

	cmdProvider := &statefulMockRunner{
		willSucceed: true,
	}

	fsProvider := &mockFileSystemProvider{
		nonExistPaths: []string{"/a/b/ssh_key", keyPathInDirectorVM},
	}

	testCases := map[string]struct {
		keyName         string
		expectedKeyPath string
		useTmpDir       bool
	}{
		"set up SSH key in BOSH director VM": {
			keyName:         "/var/vcap/some_dir/ssh_key",
			expectedKeyPath: keyPathInDirectorVM,
		},
		"set up SSH key in user working station": {
			keyName:         "/a/b/ssh_key",
			expectedKeyPath: "/a/b/ssh_key",
			useTmpDir:       false,
		},
		"set up SSH key under tmp directory in user working station": {
			keyName:         "/a/b/ssh_key",
			expectedKeyPath: "/tmp/ssh_key",
			useTmpDir:       true,
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			gotKeyPath, err := sshKeyPath(tc.keyName, cmdProvider, fsProvider, tc.useTmpDir)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if gotKeyPath != tc.expectedKeyPath {
				t.Fatalf("got key path %q, expected key path %q", gotKeyPath, tc.expectedKeyPath)
			}
		})
	}
}

func TestSSHKeyPath_Fail_SetupSSHKey(t *testing.T) {
	cmdProvider := &statefulMockRunner{
		willSucceed: false,
	}

	fsProvider := &mockFileSystemProvider{
		nonExistPaths: []string{"/a/b/ssh_key"},
	}

	if _, err := sshKeyPath("/a/b/ssh_key", cmdProvider, fsProvider, false); err == nil {
		t.Fatal("got nil, expected error")
	}
}

func TestDownload_Fail(t *testing.T) {
	mockCmdProvider = &statefulMockRunner{
		willSucceed: false,
	}
	ssh := getSSHManager(mockCmdProvider)

	if _, err := ssh.Download(context.Background(), vmName, "1.2.3.4", "foo.txt"); err == nil {
		t.Fatal("expected error, but none was given")
	}
}

func TestDownload_Fail_Connection_Timeout(t *testing.T) {
	mockCmdProvider = &statefulMockRunner{
		willSucceed: true,
	}
	ssh := getSSHManager(mockCmdProvider)
	ssh.connChecker = &mockConnChecker{
		err: errors.New("ssh connection timeout"),
	}

	if _, err := ssh.Download(context.Background(), vmName, "1.2.3.4", "foo.txt"); err != nil && !strings.Contains(err.Error(), "ssh connection timeout") {
		t.Fatalf("got unexpected error: %v", err)
	}
}

func getSSHManager(mockCmdProvider *statefulMockRunner) *Manager {
	return &Manager{
		keyPath:      "/foo/bar/baz",
		disableProxy: true,
		cmdProvider:  mockCmdProvider,
		vmService:    noOpVMService{},
		vmarService:  noOpVMARService{},
		connChecker:  &mockConnChecker{},
		waiter: wait.Waiter{
			Logger: cfboshlog.New(boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr)),
			Clock:  &clock.RealClock{},
		},
	}
}

type statefulMockRunner struct {
	willSucceed bool
	ranCommand  string
}

var _ commandProvider = &statefulMockRunner{}

func (s *statefulMockRunner) Command(name string, arg ...string) *exec.Cmd {
	s.ranCommand = fmt.Sprintf("%s %s", name, strings.Join(arg, " "))
	var cmd *exec.Cmd
	if s.willSucceed {
		cmd = exec.Command("true")
	} else {
		cmd = exec.Command("false")
	}
	return cmd
}

type noOpVMService struct{}

var _ vmChecker = noOpVMService{}

func (m noOpVMService) EnsureVMExistAndRunning(vmName string) error {
	return nil
}

type noOpVMARService struct{}

var _ vmservice.VMARAccessChecker = noOpVMARService{}

func (m noOpVMARService) EnsureVMARExistAndReady(vmName string, publicKey string) error {
	return nil
}

type mockFileSystemProvider struct {
	nonExistPaths []string
}

func (f *mockFileSystemProvider) Stat(filePath string) (os.FileInfo, error) {
	if slices.Contains(f.nonExistPaths, filePath) {
		return nil, fs.ErrNotExist
	}

	return nil, nil
}

func (f *mockFileSystemProvider) Lock(string) (func(), error) {
	return func() {}, nil
}

type mockConnChecker struct {
	err error
}

func (mc *mockConnChecker) ensureSSHConnection(ctx context.Context, host string, port string, timeout wait.Waiting) error {
	return mc.err
}

// fakeKeygenRunner stands in for ssh-keygen, including the behaviour that
// caused the outage: it refuses to overwrite an existing private key and exits
// non-zero, exactly as the real tool does when its confirmation prompt reaches
// a closed stdin. The sleep widens the window between the existence check and
// the write so the race is hit reliably rather than occasionally.
type fakeKeygenRunner struct {
	calls atomic.Int64
}

var _ commandProvider = &fakeKeygenRunner{}

// keygenTargetPath pulls the -f argument back out of a simulated ssh-keygen
// invocation.
func keygenTargetPath(arg []string) string {
	for i, a := range arg {
		if a == "-f" && i+1 < len(arg) {
			return arg[i+1]
		}
	}

	return ""
}

func (f *fakeKeygenRunner) Command(_ string, arg ...string) *exec.Cmd {
	keyPath := keygenTargetPath(arg)

	// Real ssh-keygen computes the key first and only refuses to overwrite when
	// it goes to write, so the window is the generation time, not the check.
	// Staggering that time by call makes the losers deterministic rather than
	// leaving the outcome to the scheduler.
	delay := fmt.Sprintf("%.2f", float64(f.calls.Add(1)-1)*0.1)

	return exec.Command("sh", "-c", "sleep "+delay+"; "+fmt.Sprintf(
		`if [ -e %[1]q ]; then echo "%[1]s already exists."; exit 1; fi; `+
			`echo private > %[1]q; echo public > %[1]q.pub`, keyPath))
}

func TestSSHKeyPath_ConcurrentCallersShareOneKey(t *testing.T) {
	// Reproduces a director cold start, where several CPI processes build their
	// factory at once and each tries to create the same missing key file.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ssh_key")

	const callers = 8
	runner := &fakeKeygenRunner{}
	var wg sync.WaitGroup
	errCh := make(chan error, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := sshKeyPath(keyPath, runner, &execFileSystemProvider{}, false)
			if err != nil {
				errCh <- err
				return
			}
			if got != keyPath {
				errCh <- fmt.Errorf("got key path %q, expected %q", got, keyPath)
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent caller failed: %v", err)
	}

	for _, path := range []string{keyPath, keyPath + ".pub"} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %q to exist after generation: %v", path, err)
		}
	}
}

func TestLazyManager_DefersInitializationUntilUsed(t *testing.T) {
	// Stemcell, disk and info calls build the factory but never transfer agent
	// settings, so they must neither pay for SSH setup nor fail on it.
	initializations := 0
	lazy := NewLazyManager(func() (*Manager, error) {
		initializations++
		return nil, errors.New("ssh setup exploded")
	})

	if initializations != 0 {
		t.Fatalf("got %d initializations, expected none before first use", initializations)
	}

	if _, err := lazy.Download(context.Background(), vmName, "1.2.3.4", "foo.txt"); err == nil {
		t.Error("got nil from Download, expected the deferred setup error to surface")
	}
	if err := lazy.Upload(context.Background(), vmName, "1.2.3.4", "source.txt", "target.txt"); err == nil {
		t.Error("got nil from Upload, expected the deferred setup error to surface")
	}

	if initializations != 1 {
		t.Errorf("got %d initializations, expected the first result to be cached", initializations)
	}
}

// splitWriteKeygenRunner writes the private key, pauses, then writes the public
// key, mirroring the order real ssh-keygen uses.
type splitWriteKeygenRunner struct {
	gap time.Duration
}

var _ commandProvider = splitWriteKeygenRunner{}

func (r splitWriteKeygenRunner) Command(_ string, arg ...string) *exec.Cmd {
	return exec.Command("sh", "-c", fmt.Sprintf(
		`echo private > %[1]q; sleep %[2]s; echo public > %[1]q.pub`,
		keygenTargetPath(arg), fmt.Sprintf("%.2f", r.gap.Seconds())))
}

func TestSSHKeyPath_WaitsForCompleteKeyPair(t *testing.T) {
	// Initialize reads the public key the instant sshKeyPath returns, so a
	// caller that arrives after the private key is written but before the
	// public key is must keep waiting rather than report a half-written pair.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ssh_key")
	runner := splitWriteKeygenRunner{gap: 500 * time.Millisecond}

	generated := make(chan error, 1)
	go func() {
		_, err := sshKeyPath(keyPath, runner, &execFileSystemProvider{}, false)
		generated <- err
	}()

	// Land inside the gap: the private key is on disk, the public key is not.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(keyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the private key to appear")
		}
		time.Sleep(5 * time.Millisecond)
	}

	got, err := sshKeyPath(keyPath, runner, &execFileSystemProvider{}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(got + pubKeyFileSuffix); err != nil {
		t.Errorf("sshKeyPath returned %q while its public key was still missing: %v", got, err)
	}

	if err := <-generated; err != nil {
		t.Errorf("unexpected error from the generating caller: %v", err)
	}
}

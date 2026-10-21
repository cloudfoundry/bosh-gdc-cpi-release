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

type mockConnChecker struct {
	err error
}

func (mc *mockConnChecker) ensureSSHConnection(ctx context.Context, host string, port string, timeout wait.Waiting) error {
	return mc.err
}

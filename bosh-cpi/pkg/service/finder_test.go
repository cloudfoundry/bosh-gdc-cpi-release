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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/test"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

type finderTestCase struct {
	InitObjects []client.Object
	ObjName     string
	Found       bool
	WantedObj   client.Object
}

func TestFindVM(t *testing.T) {
	ctx := context.Background()

	testCases := vmTestCases()
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			fc := test.FakeClient(tc.InitObjects...)
			finder := NewCPIFinder(fc, test.ProjectName)
			got, err := finder.FindVM(ctx, tc.ObjName)
			if err != nil && tc.Found {
				t.Fatalf("Failed to run test: %v", err)
			}
			if err == nil && !tc.Found {
				t.Fatal("test failed, got nil error, wanted NotFound error")
			}
			found := (got != nil)
			if tc.Found != found {
				t.Fatalf("test failed, got vm (%t), expected (%t)", found, tc.Found)
			}
			if tc.Found {
				if diff := cmp.Diff(tc.WantedObj, got); diff != "" {
					t.Fatalf("test failed, (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestFindVMErr(t *testing.T) {
	ctx := context.Background()
	fc := test.FakeInvalidClient()
	finder := NewCPIFinder(fc, test.ProjectName)
	got, err := finder.FindVM(ctx, test.WantedObjName)
	if err == nil {
		t.Fatal("test failed, expected to get error")
	}
	if got != nil {
		t.Fatal("test failed, expected not having object")
	}
}

func TestHasVM(t *testing.T) {
	ctx := context.Background()

	testcases := vmTestCases()
	for id, tc := range testcases {
		t.Run(id, func(t *testing.T) {
			fc := test.FakeClient(tc.InitObjects...)
			finder := NewCPIFinder(fc, test.ProjectName)
			hasObj, err := finder.HasVM(ctx, tc.ObjName)
			if err != nil {
				t.Fatalf("Failed to run test: %v", err)
			}
			if hasObj != tc.Found {
				t.Fatalf("test failed, got (%t), want (%t)", hasObj, tc.Found)
			}
		})
	}
}

func TestHasVMNotFoundErr(t *testing.T) {
	ctx := context.Background()

	fc := test.FakeClient()
	finder := NewCPIFinder(fc, test.ProjectName)
	hasObj, err := finder.HasVM(ctx, test.WantedObjName)
	if err != nil {
		t.Fatalf("test failed, got unexpected error: %v", err)
	}
	if hasObj {
		t.Fatal("test failed, expected not having object")
	}
}

func TestHasVMErr(t *testing.T) {
	ctx := context.Background()

	fc := test.FakeInvalidClient()
	finder := NewCPIFinder(fc, test.ProjectName)
	hasObj, err := finder.HasVM(ctx, test.WantedObjName)
	if err == nil {
		t.Fatal("test failed, expected to get error")
	}
	if hasObj {
		t.Fatal("test failed, expected not having object")
	}
}

func vmTestCases() map[string]finderTestCase {
	wantedVM := &vmv1.VirtualMachine{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VirtualMachine",
			APIVersion: "virtualmachine.gdc.goog/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      test.WantedObjName,
			Namespace: test.ProjectName,
		},
	}
	vm := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test2",
			Namespace: test.ProjectName,
		},
	}
	vmDiffOrg := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      test.WantedObjName,
			Namespace: "org-2",
		},
	}

	return map[string]finderTestCase{
		"not found": {
			InitObjects: []client.Object{vm, vmDiffOrg},
			ObjName:     test.WantedObjName,
			Found:       false,
			WantedObj:   nil,
		},
		"found": {
			InitObjects: []client.Object{wantedVM, vmDiffOrg},
			ObjName:     test.WantedObjName,
			Found:       true,
			WantedObj:   wantedVM,
		},
	}
}

func TestFindVMIP(t *testing.T) {
	expectedIP := "10.0.0.1"
	vm := &vmv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vm",
			Namespace: test.ProjectName,
		},
		Status: vmv1.VirtualMachineStatus{
			Network: vmv1.NetworkStatus{
				Interfaces: []vmv1.NetworkInterfaceStatus{
					{
						Name: "net0",
						IpAddresses: []string{
							"192.168.1.1",
						},
					},
					{
						Name: vmNodeNetworkLancerInterfaceName,
						IpAddresses: []string{
							"10.0.0.1/24",
						},
					},
				},
			},
		},
	}

	ctx := context.Background()
	c := test.FakeClient(vm)
	finder := NewCPIFinder(c, test.ProjectName)

	ip, err := finder.FindVMIP(ctx, "vm")
	if err != nil {
		t.Fatalf("unexpected error finding VM IP: %v", err)
	}
	if ip != expectedIP {
		t.Fatalf("got ip %q, expected ip %q", ip, expectedIP)
	}
}

func TestFindVMIP_Fail(t *testing.T) {
	testCases := map[string]struct {
		NetInterfaces []vmv1.NetworkInterfaceStatus
	}{
		"no " + vmNodeNetworkLancerInterfaceName + " network node": {
			[]vmv1.NetworkInterfaceStatus{
				{
					Name: "net0",
					IpAddresses: []string{
						"192.168.1.1",
					},
				},
			},
		},
		"invalid CIDR": {
			[]vmv1.NetworkInterfaceStatus{
				{
					Name: vmNodeNetworkLancerInterfaceName,
					IpAddresses: []string{
						"192.168.1.1.121232/sdsds",
					},
				},
			},
		},
		"empty address list in " + vmNodeNetworkLancerInterfaceName: {
			[]vmv1.NetworkInterfaceStatus{
				{
					Name:        vmNodeNetworkLancerInterfaceName,
					IpAddresses: []string{},
				},
			},
		},
	}

	ctx := context.Background()
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			vm := &vmv1.VirtualMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vm",
					Namespace: test.ProjectName,
				},
				Status: vmv1.VirtualMachineStatus{
					Network: vmv1.NetworkStatus{
						Interfaces: tc.NetInterfaces,
					},
				},
			}

			c := test.FakeClient(vm)
			finder := NewCPIFinder(c, test.ProjectName)

			if _, err := finder.FindVMIP(ctx, "vm"); err == nil {
				t.Fatal("got nil, expected error")
			}
		})
	}
}

func TestFindVMIngressIP(t *testing.T) {
	ingressIP := "10.200.8.21"
	vmea := &vmv1.VirtualMachineExternalAccess{
		ObjectMeta: metav1.ObjectMeta{
			Name:      test.WantedObjName,
			Namespace: test.ProjectName,
		},
		Status: vmv1.VirtualMachineExternalAccessStatus{
			EgressIP:  "10.200.3.3",
			IngressIP: ingressIP,
		},
	}

	ctx := context.Background()
	fc := test.FakeClient(vmea)
	finder := NewCPIFinder(fc, test.ProjectName)
	gotIP, err := finder.FindVMIngressIP(ctx, test.WantedObjName)
	if err != nil {
		t.Fatalf("test failed, got unexptected err: %v", err)
	}
	if gotIP != ingressIP {
		t.Fatalf("test failed, got %s, expected %s", gotIP, ingressIP)
	}
}

func TestFindVMIngressIPFailed(t *testing.T) {
	testCases := map[string]struct {
		vmea *vmv1.VirtualMachineExternalAccess
	}{
		"not found": {
			vmea: nil,
		},
		"empty ip": {
			vmea: &vmv1.VirtualMachineExternalAccess{
				ObjectMeta: metav1.ObjectMeta{
					Name:      test.WantedObjName,
					Namespace: test.ProjectName,
				},
			},
		},
	}

	ctx := context.Background()
	var fc client.Client
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			if tc.vmea == nil {
				fc = test.FakeClient()
			} else {
				fc = test.FakeClient(tc.vmea)
			}
			finder := NewCPIFinder(fc, test.ProjectName)
			if _, err := finder.FindVMIngressIP(ctx, test.WantedObjName); err == nil {
				t.Fatal("got nil err, expected error")
			}
		})
	}
}

func TestFindDisk(t *testing.T) {
	ctx := context.Background()

	testCases := diskTestCases()
	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			fc := test.FakeClient(tc.InitObjects...)
			finder := NewCPIFinder(fc, test.ProjectName)
			got, err := finder.FindDisk(ctx, tc.ObjName)
			if err != nil && tc.Found {
				t.Fatalf("Failed to run test: %v", err)
			}
			if err == nil && !tc.Found {
				t.Fatal("test failed, got nil error, wanted NotFound error")
			}
			found := (got != nil)
			if tc.Found != found {
				t.Fatalf("test failed, got disk (%t), expected (%t)", found, tc.Found)
			}
			if tc.Found {
				if diff := cmp.Diff(tc.WantedObj, got, cmpopts.IgnoreUnexported(vmv1.VirtualMachineDiskSpec{})); diff != "" {
					t.Fatalf("test failed, (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestFindDiskErr(t *testing.T) {
	ctx := context.Background()
	fc := test.FakeInvalidClient()
	finder := NewCPIFinder(fc, test.ProjectName)
	got, err := finder.FindDisk(ctx, test.WantedObjName)
	if err == nil {
		t.Fatal("test failed, expected to get error")
	}
	if got != nil {
		t.Fatal("test failed, expected not having object")
	}
}

func TestHasDisk(t *testing.T) {
	ctx := context.Background()

	testcases := diskTestCases()
	for id, tc := range testcases {
		t.Run(id, func(t *testing.T) {
			fc := test.FakeClient(tc.InitObjects...)
			finder := NewCPIFinder(fc, test.ProjectName)
			hasObj, err := finder.HasDisk(ctx, tc.ObjName)
			if err != nil {
				t.Fatalf("Failed to run test: %v", err)
			}
			if hasObj != tc.Found {
				t.Fatalf("test failed, wanted (%t), got (%t)", hasObj, tc.Found)
			}
		})
	}
}

func TestHasDiskNotFoundErr(t *testing.T) {
	ctx := context.Background()

	fc := test.FakeClient()
	finder := NewCPIFinder(fc, test.ProjectName)
	hasObj, err := finder.HasDisk(ctx, test.WantedObjName)
	if err != nil {
		t.Fatalf("test failed, got unexpected error: %v", err)
	}
	if hasObj {
		t.Fatal("test failed, expected not having object")
	}
}

func TestHasDiskErr(t *testing.T) {
	ctx := context.Background()

	fc := test.FakeInvalidClient()
	finder := NewCPIFinder(fc, test.ProjectName)
	hasObj, err := finder.HasDisk(ctx, test.WantedObjName)
	if err == nil {
		t.Fatal("test failed, expected to get error")
	}
	if hasObj {
		t.Fatal("test failed, expected not having object")
	}
}

func diskTestCases() map[string]finderTestCase {
	wantedDisk := &vmv1.VirtualMachineDisk{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VirtualMachineDisk",
			APIVersion: "virtualmachine.gdc.goog/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      test.WantedObjName,
			Namespace: test.ProjectName,
		},
	}
	disk := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test2",
			Namespace: test.ProjectName,
		},
	}
	diskDiffOrg := &vmv1.VirtualMachineDisk{
		ObjectMeta: metav1.ObjectMeta{
			Name:      test.WantedObjName,
			Namespace: "org-2",
		},
	}

	return map[string]finderTestCase{
		"not found": {
			InitObjects: []client.Object{wantedDisk, disk, diskDiffOrg},
			ObjName:     "my-disk",
			Found:       false,
			WantedObj:   nil,
		},
		"found": {
			InitObjects: []client.Object{wantedDisk, diskDiffOrg},
			ObjName:     test.WantedObjName,
			Found:       true,
			WantedObj:   wantedDisk,
		},
	}
}

func TestFindBackupPlans(t *testing.T) {
	ctx := context.Background()

	testcases := backupPlanFinderTestCases()
	for id, tc := range testcases {
		t.Run(id, func(t *testing.T) {
			fc := test.FakeClient(tc.InitObjects...)
			finder := NewCPIFinder(fc, test.ProjectName)
			got, err := finder.FindBackupPlans(ctx, tc.ObjName)
			if err != nil && tc.Found {
				t.Fatalf("run test: %v", err)
			}
			if err == nil && !tc.Found {
				t.Fatal("got nil error, wanted NotFound error")
			}

			var wantedObjs []vmv1.VirtualMachineBackupPlan
			if tc.WantedObj != nil {
				if vmbp, ok := tc.WantedObj.(*vmv1.VirtualMachineBackupPlan); ok {
					wantedObjs = []vmv1.VirtualMachineBackupPlan{*vmbp}
				}
			} else {
				wantedObjs = []vmv1.VirtualMachineBackupPlan{}
			}

			var actualObjs []vmv1.VirtualMachineBackupPlan
			if len(got) > 0 {
				actualObjs = []vmv1.VirtualMachineBackupPlan{got[0]}
			} else {
				actualObjs = []vmv1.VirtualMachineBackupPlan{}
			}

			if diff := cmp.Diff(wantedObjs, actualObjs, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "CreationTimestamp")); diff != "" {
				t.Fatalf("test failed, (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFindBackupPlansErr(t *testing.T) {
	ctx := context.Background()
	fc := test.FakeInvalidClient()
	finder := NewCPIFinder(fc, test.ProjectName)
	got, err := finder.FindBackupPlans(ctx, test.WantedObjName)
	if err == nil {
		t.Fatal("test failed, expected to get error")
	}
	if err != nil && !strings.Contains(err.Error(), "list backup plans for disk") {
		t.Fatalf("test failed, expected error to contain 'list backup plans for disk', got %v", err)
	}
	if got != nil {
		t.Fatal("test failed, expected not having object")
	}
}

func backupPlanFinderTestCases() map[string]finderTestCase {
	t1 := metav1.Now()
	t2 := metav1.Time{Time: t1.Add(-10 * time.Minute)}
	t3 := metav1.Time{Time: t1.Add(10 * time.Minute)}

	wantedPlan1 := &vmv1.VirtualMachineBackupPlan{
		TypeMeta: metav1.TypeMeta{Kind: "VirtualMachineBackupPlan", APIVersion: "virtualmachine.gdc.goog/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "backup-plan-old", Namespace: test.ProjectName, CreationTimestamp: t2,
			Labels: map[string]string{consts.VMBackupPlanLabelKey: "matching-disk"},
		},
	}
	wantedPlan2 := &vmv1.VirtualMachineBackupPlan{
		TypeMeta: metav1.TypeMeta{Kind: "VirtualMachineBackupPlan", APIVersion: "virtualmachine.gdc.goog/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "backup-plan-new", Namespace: test.ProjectName, CreationTimestamp: t1,
			Labels: map[string]string{consts.VMBackupPlanLabelKey: "matching-disk"},
		},
	}
	otherPlan := &vmv1.VirtualMachineBackupPlan{
		TypeMeta: metav1.TypeMeta{Kind: "VirtualMachineBackupPlan", APIVersion: "virtualmachine.gdc.goog/v1"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "backup-plan-other", Namespace: test.ProjectName, CreationTimestamp: t3,
			Labels: map[string]string{consts.VMBackupPlanLabelKey: "other-disk"},
		},
	}

	return map[string]finderTestCase{
		"not found": {
			InitObjects: []client.Object{otherPlan},
			ObjName:     "matching-disk",
			WantedObj:   nil, // Fake client returns empty slice natively, finder tests rely on WantedObj = nil
			Found:       true,
		},
		"found specific backup plan": {
			InitObjects: []client.Object{wantedPlan1, wantedPlan2, otherPlan},
			ObjName:     "matching-disk",
			WantedObj:   wantedPlan2, // Only assert match against the most recently created plan
			Found:       true,
		},
	}
}

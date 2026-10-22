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

package test

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	networkingv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/networking/v1"
	objectv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/object/v1"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

var (
	WantedObjName = "wantedObj"
	ProjectName   = "org-1"
	Timeout       = config.WaitTimeout{
		DiskCreation:     1,
		DiskDeletion:     1,
		SnapshotCreation: 1,
		SnapshotDeletion: 1,
		StemcellCreation: 1,
		StemcellDeletion: 1,
		VMDeletion:       1,
		VMStart:          1,
		VMStop:           1,
	}
)

func FakeClient(objects ...client.Object) client.WithWatch {
	scheme := runtime.NewScheme()
	utilruntime.Must(vmv1.AddToScheme(scheme))
	utilruntime.Must(networkingv1.AddToScheme(scheme))
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(objectv1.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithObjects(objects...).
		WithScheme(scheme).
		Build()
}

func FakeInvalidClient() client.Client {
	return fake.NewClientBuilder().Build()
}

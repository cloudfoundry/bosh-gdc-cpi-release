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
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/hashicorp/go-multierror"

	kubeclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
)

type MultizoneFinder struct {
	cloudClients *kubeclient.KubeClients
	projectID    string
}

func NewMultizoneFinder(cloudClients *kubeclient.KubeClients, projectID string) *MultizoneFinder {
	return &MultizoneFinder{
		cloudClients: cloudClients,
		projectID:    projectID,
	}
}

func (f *MultizoneFinder) GetZone(ctx context.Context, objName string, obj client.Object) (string, error) {
	var errs *multierror.Error
	for zone, zonalKubeClient := range f.cloudClients.ZonalAPIServers {
		err := zonalKubeClient.Get(ctx, client.ObjectKey{Name: objName, Namespace: f.projectID}, obj)
		if err == nil {
			return zone, nil
		}
		if client.IgnoreNotFound(err) != nil {
			errs = multierror.Append(errs, fmt.Errorf("zone %s, %w", zone, err))
		}
	}
	if errs != nil {
		return "", errs.ErrorOrNil()
	}
	return "", apierrors.NewNotFound(schema.GroupResource{Resource: obj.GetObjectKind().GroupVersionKind().Kind}, objName)
}

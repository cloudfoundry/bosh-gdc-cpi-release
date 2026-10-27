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

package kube

import (
	"encoding/base64"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/auth"
	cpiconfig "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	networkingv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/networking/v1"
	objectv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/object/v1"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

// KubeClients holds Kubernetes clients for interacting with GDCH global and zonal resources.
type KubeClients struct {
	// Global client used for interacting with GDCH global resources.
	GlobalAPIServer client.WithWatch `json:"globalAPIServer,omitempty"`
	// Map of zonal clients, used for interacting with GDCH zonal resources.
	ZonalAPIServers map[string]client.WithWatch `json:"zonalAPIServers,omitempty"`
}

func GetKubeClients(gdcConfig *cpiconfig.GDCConfig) (*KubeClients, error) {
	// Initialize global client
	globalAPIEndpoint := gdcConfig.OrgAPIEndpoints.GlobalAPI
	if globalAPIEndpoint == "" {
		globalAPIEndpoint = gdcConfig.OrgAdminURL
	}

	// Initialize global client
	globalClient, err := GetKubeClient(gdcConfig, globalAPIEndpoint)
	if err != nil {
		return nil, fmt.Errorf("get global Kubernetes client: %w", err)
	}

	// Initialize zonal clients based on available zones.
	zonalClients := make(map[string]client.WithWatch)
	for zone, endpoint := range gdcConfig.OrgAPIEndpoints.ZonalAPIs {
		if endpoint == "" {
			continue // Skip creating a client if the endpoint is empty
		}

		zonalClient, err := GetKubeClient(gdcConfig, endpoint)
		if err != nil {
			return nil, fmt.Errorf("get zonal Kubernetes client for zone %q: %w", zone, err)
		}
		zonalClients[zone] = zonalClient
	}

	return &KubeClients{
		GlobalAPIServer: globalClient,
		ZonalAPIServers: zonalClients,
	}, nil
}

func GetKubeClient(gdcConfig *cpiconfig.GDCConfig, apiEndpoint string) (client.WithWatch, error) {
	// Preserve existing call signatures by using OrgAdminURL as the default when apiEndpoint is not provided.
	if apiEndpoint == "" {
		apiEndpoint = gdcConfig.OrgAdminURL
	}

	restConfig, err := getRESTConfig(gdcConfig, apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("retrieving Kubernetes REST configuration: %w", err)
	}

	c, err := client.NewWithWatch(restConfig, client.Options{
		Scheme: runtime.NewScheme(),
	})
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client from config: %w", err)
	}

	if err := vmv1.AddToScheme(c.Scheme()); err != nil {
		return nil, fmt.Errorf("add vm types to kubernetes client: %w", err)
	}
	if err := networkingv1.AddToScheme(c.Scheme()); err != nil {
		return nil, fmt.Errorf("add networking types to kubernetes client: %w", err)
	}

	if err := objectv1.AddToScheme(c.Scheme()); err != nil {
		return nil, fmt.Errorf("add object storage types to kubernetes client: %w", err)
	}

	if err := corev1.AddToScheme(c.Scheme()); err != nil {
		return nil, fmt.Errorf("add 'k8s.io/api/core/v1' types to kubernetes client: %w", err)
	}

	return c, nil
}

func getRESTConfig(gdcConfig *cpiconfig.GDCConfig, apiEndpoint string) (*rest.Config, error) {
	certData, err := base64.StdEncoding.DecodeString(gdcConfig.CAData)
	if err != nil {
		return nil, fmt.Errorf("decode cadata: %w", err)
	}

	stsTS := auth.NewCachedSTSTokenSource(certData, apiEndpoint, &gdcConfig.ServiceAccountKey)
	cfg := &rest.Config{
		Host: apiEndpoint,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: certData,
		},
	}

	// Injects STS tokens as the bearer authorization header in the requests.
	cfg.Wrap(transport.TokenSourceWrapTransport(stsTS))

	return cfg, nil
}

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

package metadata

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/auth"
	cpiconfig "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"golang.org/x/oauth2"
)

const (
	metadataServerScheme    = "https://"
	metadataServerSubdomain = "metadata."
	metadataServerUrlPath   = "/virtual-machines/metadata/v1/projects/"
	tokenUriPrefix          = "https://service-accounts."
	boshSettingsKey         = "bosh-settings"
	contentTypeHeader       = "Content-Type"
	applicationJSON         = "application/json"
)

type MetadataClient struct {
	ProjectID string
	OrgSuffix string
	Client    *http.Client
}

type metadataResponse struct {
	Metadata struct {
		Items []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"items"`
	} `json:"metadata"`
}

func GetMetadataClient(gdcConfig *cpiconfig.GDCConfig, zone ...string) (*MetadataClient, error) {
	var orgSuffix string
	var err error
	switch len(zone) {
	case 1:
		zone := zone[0]
		if orgSuffix, err = getOrgSuffix(gdcConfig, zone); err != nil {
			return nil, fmt.Errorf("get org suffix for zone %q: %w", zone, err)
		}
	case 0:
		if orgSuffix, err = getOrgSuffix(gdcConfig); err != nil {
			return nil, fmt.Errorf("get org suffix: %w", err)
		}
	default:
		return nil, fmt.Errorf("GetMetadataClient expects at most one zone argument, got %d", len(zone))
	}

	projectID := gdcConfig.ServiceAccountKey.Project

	certData, err := base64.StdEncoding.DecodeString(gdcConfig.CAData)
	if err != nil {
		return nil, fmt.Errorf("decode base64 CA data: %w", err)
	}

	baseHttpClient, err := createHTTPClientWithCustomCA(certData)
	if err != nil {
		return nil, fmt.Errorf("create base HTTP client: %w", err)
	}

	metadataServerUrl := fmt.Sprintf("%s%s%s", metadataServerScheme, metadataServerSubdomain, orgSuffix)

	stsToken := auth.NewCachedSTSTokenSource(certData, metadataServerUrl, &gdcConfig.ServiceAccountKey)

	oauthClient := oauth2.NewClient(context.Background(), stsToken)
	oauthClient.Transport = &oauth2.Transport{
		Source: stsToken,
		Base:   baseHttpClient.Transport,
	}

	return &MetadataClient{
		ProjectID: projectID,
		OrgSuffix: orgSuffix,
		Client:    oauthClient,
	}, nil
}

func (m *MetadataClient) Fetch(ctx context.Context, vmID string) (apiv1.AgentEnv, error) {
	if vmID == "" {
		return nil, errors.New("vmID not provided")
	}

	url := fmt.Sprintf("%s%s%s%s%s/vm/%s", metadataServerScheme, metadataServerSubdomain, m.OrgSuffix, metadataServerUrlPath, m.ProjectID, vmID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}

	req.Header.Set(contentTypeHeader, applicationJSON)

	httpResp, err := m.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send HTTP request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read HTTP response body: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET request to %s failed with status: %d, body: %s", url, httpResp.StatusCode, respBody)
	}

	if len(respBody) == 0 {
		return nil, fmt.Errorf("empty response from server")
	}

	var response metadataResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, fmt.Errorf("unmarshal JSON response: %w", err)
	}

	var boshSettingsValue string
	for _, item := range response.Metadata.Items {
		if item.Key == boshSettingsKey {
			boshSettingsValue = item.Value
			break
		}
	}

	if boshSettingsValue == "" {
		return nil, fmt.Errorf("missing bosh settings value in response")
	}

	agentEnv, err := apiv1.AgentEnvFactory{}.FromBytes([]byte(boshSettingsValue))
	if err != nil {
		return nil, fmt.Errorf("unmarshal agent env from extracted value: %w", err)
	}

	return agentEnv, nil
}

func (m *MetadataClient) Update(ctx context.Context, vmID string, agentEnv apiv1.AgentEnv) error {
	if vmID == "" {
		return errors.New("vmID not provided")
	}

	agentEnvBytes, err := agentEnv.AsBytes()
	if err != nil {
		return fmt.Errorf("marshal agent env: %w", err)
	}

	payload := map[string]interface{}{
		"metadata": map[string][]map[string]string{
			"items": {
				{
					"key":   "bosh-settings",
					"value": string(agentEnvBytes),
				},
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal JSON payload: %w", err)
	}

	url := fmt.Sprintf("%s%s%s%s%s/vm/%s", metadataServerScheme, metadataServerSubdomain, m.OrgSuffix, metadataServerUrlPath, m.ProjectID, vmID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("create HTTP request: %w", err)
	}

	req.Header.Set(contentTypeHeader, applicationJSON)

	httpResp, err := m.Client.Do(req)
	if err != nil {
		return fmt.Errorf("send HTTP request: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("read HTTP response body: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST request to %s failed with status: %d, body: %s", url, httpResp.StatusCode, respBody)
	}

	return nil
}

func getOrgSuffix(gdcConfig *cpiconfig.GDCConfig, zone ...string) (string, error) {
	var apiEndpoint string
	if len(zone) > 0 {
		zone := zone[0]
		var ok bool
		if apiEndpoint, ok = gdcConfig.OrgAPIEndpoints.ZonalAPIs[zone]; !ok {
			return "", fmt.Errorf("zone %q missing from zonal APIs: %v", zone, gdcConfig.OrgAPIEndpoints.ZonalAPIs)
		}
		if apiEndpoint == "" {
			return "", fmt.Errorf("zone %q exists in zonal APIs but endpoint is empty: %v", zone, gdcConfig.OrgAPIEndpoints.ZonalAPIs)
		}
	} else {
		apiEndpoint = gdcConfig.OrgAdminURL
	}

	index := strings.Index(apiEndpoint, gdcConfig.Org)

	if index == -1 {
		return "", fmt.Errorf("org value %q not found in endpoint %q", gdcConfig.Org, apiEndpoint)
	}

	return apiEndpoint[index:], nil
}

func createHTTPClientWithCustomCA(certData []byte) (*http.Client, error) {
	if len(certData) == 0 {
		return &http.Client{Transport: http.DefaultTransport}, nil
	}

	caCertPool := x509.NewCertPool()
	if ok := caCertPool.AppendCertsFromPEM(certData); !ok {
		return nil, errors.New("parse CA certificates from PEM data")
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: caCertPool,
		},
		Proxy: http.ProxyFromEnvironment,
	}

	return &http.Client{Transport: transport}, nil
}

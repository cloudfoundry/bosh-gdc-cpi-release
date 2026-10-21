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

package integration

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"github.com/cloudfoundry/bosh-cpi-go/rpc"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	"github.com/cloudfoundry/bosh-utils/system"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	cpiconfig "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/factory"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
)

const (
	logTag = "integration_main"
)

var (
	configPathOpt = flag.String("configPath", "", "Path to configuration file")
	logger        = boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr)
	zones         = []string{}
	useStemcell   = false

	executor    *cpiExecutor
	cloudClient client.WithWatch
	kubeClients *kubeclient.KubeClients
	projectID   string
	gdcConfig   *cpiconfig.GDCConfig
)

type mzTestConfig struct {
	zone string
	vmId string
}

func TestMain(m *testing.M) {
	fs := system.NewOsFileSystem(boshlog.NewWriterLogger(boshlog.LevelInfo, os.Stderr))
	defer logger.HandlePanic(logTag)

	flag.Parse()

	config, err := cpiconfig.NewConfigFromPath(*configPathOpt, fs)
	if err != nil {
		logger.Error(logTag, "Loading config %s", err.Error())
		os.Exit(1)
	}
	gdcConfig = &config.GDC
	projectID = config.GDC.ServiceAccountKey.Project
	useStemcell = gdcConfig.StemcellInfo.ImagePath != "" && gdcConfig.StemcellInfo.ManifestPath != ""

	cloudClient, err = kubeclient.GetKubeClient(&config.GDC, "")
	if err != nil {
		logger.Error(logTag, "Get client %s", err.Error())
		os.Exit(1)
	}

	ctx, err := getCallContext(&config.GDC)
	if err != nil {
		logger.Error(logTag, "Get call context %s", err.Error())
		os.Exit(1)
	}

	kubeClients, err = kubeclient.GetKubeClients(&config.GDC)
	if err != nil {
		logger.Error(logTag, "Get multizone kubeclients %s", err.Error())
		os.Exit(1)
	}
	availableZones := []string{}
	for zone := range gdcConfig.OrgAPIEndpoints.ZonalAPIs {
		availableZones = append(availableZones, zone)
	}
	zones = availableZones

	cpiFactory := factory.NewFactory(cloudClient, config.Agent, config, cfboshlog.New(logger))

	rpcFactory := rpc.NewFactory(logger)
	executor = &cpiExecutor{
		rpcFactory: &rpcFactory,
		cpiFactory: &cpiFactory,
		ctx:        ctx,
	}

	os.Exit(m.Run())
}

func getCallContext(gdcConfig *cpiconfig.GDCConfig) (*apiv1.CloudPropsImpl, error) {
	configMap := map[string]string{
		"org":                gdcConfig.Org,
		"project":            gdcConfig.ServiceAccountKey.Project,
		"serviceAccountName": gdcConfig.ServiceAccountKey.ServiceAccountName,
	}
	jsonData, err := json.Marshal(configMap)
	if err != nil {
		return nil, err
	}

	return &apiv1.CloudPropsImpl{
		RawMessage: json.RawMessage(jsonData),
	}, nil
}

func getKubeClient(mzConfig ...mzTestConfig) client.WithWatch {
	targetedZone := ""
	if len(mzConfig) > 0 {
		targetedZone = mzConfig[0].zone
	}

	return kubeClients.ZonalAPIServers[targetedZone]
}

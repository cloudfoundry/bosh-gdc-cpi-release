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

package factory

import (
	"fmt"

	apiv1 "github.com/cloudfoundry/bosh-cpi-go/apiv1"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	"github.com/cloudfoundry/bosh-utils/uuid"
	"k8s.io/utils/clock"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/agent"
	kubeclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	metadataclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/metadata"
	cpiconfig "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/disk"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/misc"
	vmservice "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/vm"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/ssh"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/stemcell"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/vm"
)

type Factory struct {
	cloudClient  client.WithWatch
	agentOptions apiv1.AgentOptions
	logger       cfboshlog.Logger
	config       *cpiconfig.Config
}

// CPI is the implementation of apiv1.CPI:
// https://github.com/cloudfoundry/bosh-cpi-go/blob/master/apiv1/interfaces.go#L11
// Its struct can be defined freely as needed, but the struct components must implement all required interfact methods.
//
//	Here CPI is defined in a way by grouping and delegating methods to different component.
type MultizoneCPI struct {
	*vm.MultizoneVMs
	*disk.MultizoneDisks
	*stemcell.MultizoneStemcells
	*misc.Misc
}

var (
	_ apiv1.CPI        = MultizoneCPI{}
	_ apiv1.CPIFactory = Factory{}
)

func NewFactory(
	c client.WithWatch,
	agentOptions apiv1.AgentOptions,
	config *cpiconfig.Config,
	logger cfboshlog.Logger,
) Factory {
	return Factory{
		cloudClient:  c,
		agentOptions: agentOptions,
		config:       config,
		logger:       logger,
	}
}

func (f Factory) New(_ apiv1.CallContext) (apiv1.CPI, error) {
	return f.multiZoneFactory()
}

func (f Factory) multiZoneFactory() (apiv1.CPI, error) {
	uuidGen := uuid.NewGenerator()
	projectId := f.config.GDC.ServiceAccountKey.Project
	kubeClients, err := kubeclient.GetKubeClients(&f.config.GDC)
	if err != nil {
		return nil, fmt.Errorf("get kube clients: %w", err)
	}
	if len(kubeClients.ZonalAPIServers) == 0 {
		return nil, fmt.Errorf("get kube clients: No zones are provided")
	}
	useMetadataServer := f.config.GDC.UseMetadataServer
	useDiskHotplug := f.config.GDC.UseDiskHotplug
	useCloudNATGateways := f.config.GDC.UseCloudNATGateways
	useTier1Network := f.config.GDC.UseTier1Network
	if useMetadataServer {
		f.logger.Info("Factory Info", "CPI will use Metadata Server for multizone deployment")
	} else {
		f.logger.Info("Factory Info", "CPI will use local file system instead of metadata server since `use_metadata_server` is not set to `true` in CPI config")
	}

	zonalAgentEnvServices := make(map[string]agent.AgentEnvService)
	zonalVMAccessServices := make(map[string]vmservice.VMARAccessChecker)
	metadataClients := make(map[string]*metadataclient.MetadataClient)
	for zone, zonalKubeClient := range kubeClients.ZonalAPIServers {
		waiter := &wait.Waiter{
			Client:    zonalKubeClient,
			Clock:     &clock.RealClock{},
			Logger:    f.logger,
			ProjectID: projectId,
		}
		zonalVMAccessServices[zone] = vmservice.NewVMAccessRequestService(zonalKubeClient, projectId, ssh.DefaultSSHUser, waiter)
		sshManager, err := ssh.Initialize(f.config.GDC.DisableSSHProxy, f.config.GDC.UseTmpDirForSSHKey, zonalKubeClient, projectId, *waiter, zonalVMAccessServices[zone])
		if err != nil {
			return nil, fmt.Errorf("initialize ssh config: %w", err)
		}
		agentEnvService := &agent.AgentEnvService{
			SSHManager: sshManager,
		}
		zonalAgentEnvServices[zone] = *agentEnvService

		var zonalMetadataClient *metadataclient.MetadataClient
		if useMetadataServer {
			zonalMetadataClient, err = metadataclient.GetMetadataClient(&f.config.GDC, zone)
			if err != nil {
				return nil, fmt.Errorf("get metadata client for zone %s: %w", zone, err)
			}
			metadataClients[zone] = zonalMetadataClient
		}
	}
	agentEnvServices := &agent.AgentEnvServices{
		ZonalAgentEnvServices: zonalAgentEnvServices,
	}
	multizoneStemcells, err := stemcell.NewMultizoneStemcells(
		kubeClients,
		uuidGen,
		f.logger,
		f.config)
	if err != nil {
		return nil, fmt.Errorf("initialize multizone stemcell service: %w", err)
	}
	return MultizoneCPI{
		vm.NewMultizoneVMs(
			kubeClients,
			f.logger,
			uuidGen,
			f.agentOptions,
			agentEnvServices,
			f.config,
			useMetadataServer,
			metadataClients,
			useDiskHotplug,
			useCloudNATGateways,
			useTier1Network,
			zonalVMAccessServices,
		),
		disk.NewMultizoneDisks(
			kubeClients,
			f.logger,
			uuidGen,
			agentEnvServices,
			f.config,
			useMetadataServer,
			useDiskHotplug,
			metadataClients),
		multizoneStemcells,
		misc.NewMisc(),
	}, nil
}

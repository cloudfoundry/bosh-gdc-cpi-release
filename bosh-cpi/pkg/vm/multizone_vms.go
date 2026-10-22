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

package vm

import (
	"context"
	"fmt"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	"k8s.io/utils/clock"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/agent"
	kubeclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/metadata"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/image"
	vmservice "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/vm"
)

type VMCPI interface {
	CreateVM(apiv1.AgentID, apiv1.StemcellCID, apiv1.VMCloudProps, apiv1.Networks, []apiv1.DiskCID, apiv1.VMEnv) (apiv1.VMCID, error)
	DeleteVM(apiv1.VMCID) error
	CalculateVMCloudProperties(apiv1.VMResources) (apiv1.VMCloudProps, error)
	SetVMMetadata(apiv1.VMCID, apiv1.VMMeta) error
	HasVM(apiv1.VMCID) (bool, error)
	RebootVM(apiv1.VMCID) error
	apiv1.VMsV2Additions
}

type MultizoneVMs struct {
	cloudClients          *kubeclient.KubeClients
	logger                cfboshlog.Logger
	zonalVMs              map[string]VMCPI
	multizoneFinder       *service.MultizoneFinder
	leaderZone            string
	zonalVMAccessServices map[string]vmservice.VMARAccessChecker
}

func NewMultizoneVMs(
	cloudClients *kubeclient.KubeClients,
	logger cfboshlog.Logger,
	uuidGen uuidGenerator,
	agentOptions apiv1.AgentOptions,
	agentEnvServices *agent.AgentEnvServices,
	config *config.Config,
	useMetadataServer bool,
	metadataClients map[string]*metadata.MetadataClient,
	useDiskHotplug bool,
	useCloudNATGateways bool,
	useTier1Network bool,
	zonalVMAccessServices map[string]vmservice.VMARAccessChecker,
) *MultizoneVMs {
	projectId := config.GDC.ServiceAccountKey.Project
	timeout := &config.GDC.Timeout
	clock := &clock.RealClock{}
	zonalVMs := make(map[string]VMCPI)

	for zone, zonalKubeClient := range cloudClients.ZonalAPIServers {
		finder := service.NewCPIFinder(zonalKubeClient, projectId)
		waiter := &wait.Waiter{
			Client:    zonalKubeClient,
			Clock:     clock,
			Logger:    logger,
			ProjectID: projectId,
		}
		imageClient := &image.Client{
			Client:    zonalKubeClient,
			ProjectID: config.GDC.ImageProject,
			Logger:    logger,
			Waiter:    waiter,
		}
		zonalVMAccessService := zonalVMAccessServices[zone]
		var zonalMetadataClient *metadata.MetadataClient
		if useMetadataServer {
			zonalMetadataClient = metadataClients[zone]
		}
		agentEnvService := agentEnvServices.ZonalAgentEnvServices[zone]
		zonalVM := NewVMs(
			zonalKubeClient,
			projectId,
			uuidGen,
			agentOptions,
			config.GDC.ExternalAccessPorts,
			logger,
			finder,
			&agentEnvService,
			imageClient,
			waiter,
			timeout,
			useMetadataServer,
			zonalMetadataClient,
			useDiskHotplug,
			useCloudNATGateways,
			useTier1Network,
			zonalVMAccessService,
		)
		zonalVMs[zone] = zonalVM
	}

	leaderZone := ""
	for zone, apiEndpoint := range config.GDC.OrgAPIEndpoints.ZonalAPIs {
		if apiEndpoint == config.GDC.OrgAdminURL {
			leaderZone = zone
			break
		}
	}

	return &MultizoneVMs{
		cloudClients:    cloudClients,
		logger:          logger,
		zonalVMs:        zonalVMs,
		multizoneFinder: service.NewMultizoneFinder(cloudClients, projectId),
		leaderZone:      leaderZone,
	}
}

func (v *MultizoneVMs) CreateVM(
	agentID apiv1.AgentID, stemcellCID apiv1.StemcellCID,
	cloudProps apiv1.VMCloudProps, networks apiv1.Networks,
	associatedDiskCIDs []apiv1.DiskCID, env apiv1.VMEnv) (apiv1.VMCID, error) {
	vmID, _, err := v.CreateVMV2(agentID, stemcellCID, cloudProps, networks, associatedDiskCIDs, env)
	if err != nil {
		return apiv1.VMCID{}, err
	}
	return vmID, nil
}

func (v *MultizoneVMs) CreateVMV2(
	agentID apiv1.AgentID, stemcellCID apiv1.StemcellCID,
	cloudProps apiv1.VMCloudProps, networks apiv1.Networks,
	associatedDiskCIDs []apiv1.DiskCID, env apiv1.VMEnv) (apiv1.VMCID, apiv1.Networks, error) {
	var prop Property
	if err := cloudProps.As(&prop); err != nil {
		return apiv1.VMCID{}, apiv1.Networks{}, fmt.Errorf("parse multizone vm cloud props: %w", err)
	}
	if err := prop.MultizoneValidate(); err != nil {
		return apiv1.VMCID{}, apiv1.Networks{}, fmt.Errorf("validate vm cloud props: %w", err)
	}
	zone := prop.Zone
	zonalVM, ok := v.zonalVMs[zone]
	if !ok {
		return apiv1.VMCID{}, apiv1.Networks{}, fmt.Errorf("unknown zone: %s", zone)
	}
	return zonalVM.CreateVMV2(agentID, stemcellCID, cloudProps, networks, associatedDiskCIDs, env)
}

func (v *MultizoneVMs) DeleteVM(cid apiv1.VMCID) error {
	vm := &vmv1.VirtualMachine{}
	zone, err := v.multizoneFinder.GetZone(context.Background(), cid.AsString(), vm)
	if apierrors.IsNotFound(err) {
		v.logger.Debug("DeleteVM", "vm %s does not exist, no-op", cid.AsString())
		return nil
	} else if err != nil {
		return err
	}
	zonalVM := v.zonalVMs[zone]
	return zonalVM.DeleteVM(cid)
}

func (v *MultizoneVMs) CalculateVMCloudProperties(res apiv1.VMResources) (apiv1.VMCloudProps, error) {
	if len(v.leaderZone) == 0 {
		return apiv1.VMCloudPropsImpl{}, fmt.Errorf("leader zone not found in configuration")
	}
	leaderVM := v.zonalVMs[v.leaderZone]
	return leaderVM.CalculateVMCloudProperties(res)
}

func (v *MultizoneVMs) SetVMMetadata(cid apiv1.VMCID, metadata apiv1.VMMeta) error {
	vm := &vmv1.VirtualMachine{}
	zone, err := v.multizoneFinder.GetZone(context.Background(), cid.AsString(), vm)
	if err != nil {
		return err
	}
	zonalVM := v.zonalVMs[zone]
	return zonalVM.SetVMMetadata(cid, metadata)
}

func (v *MultizoneVMs) HasVM(cid apiv1.VMCID) (bool, error) {
	vm := &vmv1.VirtualMachine{}
	if _, err := v.multizoneFinder.GetZone(context.Background(), cid.AsString(), vm); apierrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("find vm: %w", err)
	}
	return true, nil
}

func (v *MultizoneVMs) RebootVM(cid apiv1.VMCID) error {
	vm := &vmv1.VirtualMachine{}
	zone, err := v.multizoneFinder.GetZone(context.Background(), cid.AsString(), vm)
	if err != nil {
		return err
	}
	zonalVM := v.zonalVMs[zone]
	return zonalVM.RebootVM(cid)
}

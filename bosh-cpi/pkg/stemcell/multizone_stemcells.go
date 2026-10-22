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

package stemcell

import (
	"context"
	"fmt"
	"time"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	"k8s.io/utils/clock"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"

	cpiconfig "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/storage"

	"github.com/cloudfoundry/bosh-utils/uuid"

	kubeclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/image"
)

type MultizoneStemcells struct {
	cloudClients   *kubeclient.KubeClients
	logger         cfboshlog.Logger
	zonalStemcells map[string]*Stemcells
	leaderZone     string
}

func NewMultizoneStemcells(cloudClients *kubeclient.KubeClients, uuidGen uuid.Generator, logger cfboshlog.Logger, config *cpiconfig.Config) (*MultizoneStemcells, error) {
	projectId := config.GDC.ServiceAccountKey.Project
	timeout := &config.GDC.Timeout
	clock := &clock.RealClock{}
	zonalStemcells := make(map[string]*Stemcells)
	for zone, zonalKubeClient := range cloudClients.ZonalAPIServers {
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
		storage, err := storage.NewGDCStorage(
			zonalKubeClient,
			logger,
			projectId,
			config.GDC.ServiceAccountKey.ServiceAccountName,
			config.GDC.CAData,
			&config.GDC.StorageCredential,
		)
		if err != nil {
			return nil, fmt.Errorf("initiate storage service: %w", err)
		}
		zonalStemcell := NewStemcells(zonalKubeClient, projectId, storage, imageClient, uuidGen, logger, timeout, config.GDC.RefreshImageImport)
		zonalStemcells[zone] = zonalStemcell
	}
	var leaderZone string
	for zone, apiEndpoint := range config.GDC.OrgAPIEndpoints.ZonalAPIs {
		if apiEndpoint == config.GDC.OrgAdminURL {
			leaderZone = zone
			break
		}
	}
	return &MultizoneStemcells{
		cloudClients:   cloudClients,
		logger:         logger,
		zonalStemcells: zonalStemcells,
		leaderZone:     leaderZone,
	}, nil
}

func (s *MultizoneStemcells) CreateStemcell(imagePath string, stemcellCloudProps apiv1.StemcellCloudProps) (apiv1.StemcellCID, error) {
	leaderZoneStemcell := s.zonalStemcells[s.leaderZone]
	s.logger.Info("CreateStemcell", "Create stemcell in leader zone %q", s.leaderZone)
	stemcellCID, err := leaderZoneStemcell.CreateStemcell(imagePath, stemcellCloudProps)
	if err != nil {
		return apiv1.StemcellCID{}, fmt.Errorf("Create stemcell in leader zone %q: %w", s.leaderZone, err)
	}

	ctx := context.Background()
	for zone, stemcell := range s.zonalStemcells {
		if zone == s.leaderZone {
			s.logger.Info("Skipping leader zone %q stemcell image check", zone)
			continue
		}
		s.logger.Info("CreateStemcell", "Confirm stemcell %q exists in zone %q", stemcellCID.AsString(), zone)
		if err := stemcell.imageService.WaitUntilVMImageCreation(ctx, stemcellCID.AsString(), time.Duration(stemcell.timeout.StemcellReplication)*time.Minute); err != nil {
			return apiv1.StemcellCID{}, fmt.Errorf("Replication of image %q in zone %q: %w", stemcellCID.AsString(), zone, err)
		}
		//add the custom CPI label to the zonal image whenever the multizone createstemcell CPI call is made
		if err := stemcell.addLabelToImage(ctx, stemcellCID.AsString()); err != nil {
			return apiv1.StemcellCID{}, fmt.Errorf("adding label to zonal virtual machine image %q in zone %q: %w", stemcellCID.AsString(), zone, err)
		}
	}
	return stemcellCID, nil
}

func (s *MultizoneStemcells) DeleteStemcell(cid apiv1.StemcellCID) error {
	for zone, stemcell := range s.zonalStemcells {
		s.logger.Info("DeleteStemcell", "Delete stemcell %q in zone %q", cid.AsString(), zone)
		if err := stemcell.DeleteStemcell(cid); err != nil {
			return fmt.Errorf("Delete stemcell %q in zone %q: %w", cid.AsString(), zone, err)
		}
	}
	return nil
}

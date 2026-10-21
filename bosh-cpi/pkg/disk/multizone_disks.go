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

package disk

import (
	"context"
	"fmt"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	"k8s.io/utils/clock"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"github.com/cloudfoundry/bosh-utils/uuid"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/agent"
	kubeclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/metadata"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service"
	vmservice "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/service/vm"
	vmv1 "github.com/googlecloudplatform/google-distributed-cloud-apis/pkg/apis/public/virtualmachine/v1"
)

type DiskCPI interface {
	apiv1.DisksV1
	apiv1.DisksV2Additions
	apiv1.SnapshotsV1
	GetDisks(cid apiv1.VMCID) ([]apiv1.DiskCID, error)
}

type MultizoneDisks struct {
	cloudClients    *kubeclient.KubeClients
	logger          cfboshlog.Logger
	zonalDisks      map[string]DiskCPI
	multizoneFinder *service.MultizoneFinder
}

// TODO (b/409931584): add multizone support for using VMM Metadata Server
func NewMultizoneDisks(
	cloudClients *kubeclient.KubeClients,
	logger cfboshlog.Logger,
	uuidGen uuid.Generator,
	agentEnvServices *agent.AgentEnvServices,
	config *config.Config,
	useMetadataServer bool,
	useDiskHotplug bool,
	metadataClients map[string]*metadata.MetadataClient,
) *MultizoneDisks {
	projectId := config.GDC.ServiceAccountKey.Project
	timeout := &config.GDC.Timeout
	clock := &clock.RealClock{}
	zonalDisks := make(map[string]DiskCPI)
	for zone, zonalKubeClient := range cloudClients.ZonalAPIServers {
		finder := service.NewCPIFinder(zonalKubeClient, projectId)
		waiter := &wait.Waiter{
			Client:    zonalKubeClient,
			Clock:     clock,
			Logger:    logger,
			ProjectID: projectId,
		}
		vmService := &vmservice.VMService{
			Client:    zonalKubeClient,
			ProjectID: projectId,
			Waiter:    waiter,
			Timeout:   timeout,
		}
		var zonalMetadataClient *metadata.MetadataClient
		if useMetadataServer {
			zonalMetadataClient = metadataClients[zone]
		}
		agentEnvService := agentEnvServices.ZonalAgentEnvServices[zone]
		zonalDisk := NewDisks(
			zonalKubeClient,
			projectId,
			finder,
			logger,
			uuidGen,
			&agentEnvService,
			waiter,
			vmService,
			timeout,
			config.GDC.EnableHotDiskExpansion,
			useMetadataServer,
			useDiskHotplug,
			zonalMetadataClient)
		zonalDisks[zone] = zonalDisk
	}
	return &MultizoneDisks{
		cloudClients:    cloudClients,
		logger:          logger,
		zonalDisks:      zonalDisks,
		multizoneFinder: service.NewMultizoneFinder(cloudClients, projectId),
	}
}

func (d *MultizoneDisks) GetDisks(cid apiv1.VMCID) ([]apiv1.DiskCID, error) {
	vmZone, err := d.multizoneFinder.GetZone(context.Background(), cid.AsString(), &vmv1.VirtualMachine{})
	if err != nil {
		return []apiv1.DiskCID{}, fmt.Errorf("find vm: %w", err)
	}
	zonalDisk := d.zonalDisks[vmZone]
	return zonalDisk.GetDisks(cid)
}

func (d *MultizoneDisks) CreateDisk(size int,
	cloudProps apiv1.DiskCloudProps,
	associatedVMCID *apiv1.VMCID) (apiv1.DiskCID, error) {
	var prop Property
	if err := cloudProps.As(&prop); err != nil {
		return apiv1.DiskCID{}, fmt.Errorf("parse disk cloud props: %w", err)
	}
	if associatedVMCID.AsString() != "" {
		vmZone, err := d.multizoneFinder.GetZone(context.Background(), associatedVMCID.AsString(), &vmv1.VirtualMachine{})
		if err != nil {
			return apiv1.DiskCID{}, fmt.Errorf("find vm: %w", err)
		}
		prop.Zone = vmZone
	}
	if err := prop.multizoneValidate(); err != nil {
		return apiv1.DiskCID{}, fmt.Errorf("validate disk cloud props: %w", err)
	}
	zone := prop.Zone
	zonalDisk, ok := d.zonalDisks[zone]
	if !ok {
		return apiv1.DiskCID{}, fmt.Errorf("unknown zone: %s", zone)
	}
	diskCID, err := zonalDisk.CreateDisk(size, cloudProps, associatedVMCID)
	if err != nil {
		return apiv1.DiskCID{}, err
	}
	return diskCID, nil
}

func (d *MultizoneDisks) DeleteDisk(cid apiv1.DiskCID) error {
	disk := &vmv1.VirtualMachineDisk{}
	zone, err := d.multizoneFinder.GetZone(context.Background(), cid.AsString(), disk)
	if apierrors.IsNotFound(err) {
		d.logger.Debug("DeleteDisk", "disk %s does not exist, no-op", cid.AsString())
		return nil
	} else if err != nil {
		return err
	}
	zonalDisk := d.zonalDisks[zone]
	return zonalDisk.DeleteDisk(cid)
}

func (d *MultizoneDisks) AttachDisk(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) error {
	_, err := d.AttachDiskV2(vmCID, diskCID)
	return err
}

func (d *MultizoneDisks) AttachDiskV2(vmCID apiv1.VMCID,
	diskCID apiv1.DiskCID) (apiv1.DiskHint, error) {
	zone, err := d.getCommonZone(vmCID, diskCID)
	if err != nil {
		return apiv1.DiskHint{}, err
	}
	zonalDisk := d.zonalDisks[zone]
	return zonalDisk.AttachDiskV2(vmCID, diskCID)
}

func (d *MultizoneDisks) DetachDisk(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) error {
	zone, err := d.getCommonZone(vmCID, diskCID)
	if err != nil {
		return err
	}
	zonalDisk := d.zonalDisks[zone]
	return zonalDisk.DetachDisk(vmCID, diskCID)
}

func (d *MultizoneDisks) HasDisk(cid apiv1.DiskCID) (bool, error) {
	if _, err := d.multizoneFinder.GetZone(context.Background(), cid.AsString(), &vmv1.VirtualMachineDisk{}); apierrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("find disk: %w", err)
	}
	return true, nil
}

func (d *MultizoneDisks) ResizeDisk(cid apiv1.DiskCID, size int) error {
	diskZone, err := d.multizoneFinder.GetZone(context.Background(), cid.AsString(), &vmv1.VirtualMachineDisk{})
	if err != nil {
		return fmt.Errorf("find disk: %w", err)
	}
	zonalDisk := d.zonalDisks[diskZone]
	return zonalDisk.ResizeDisk(cid, size)
}

func (d *MultizoneDisks) SetDiskMetadata(cid apiv1.DiskCID,
	metadata apiv1.DiskMeta) error {
	diskZone, err := d.multizoneFinder.GetZone(context.Background(), cid.AsString(), &vmv1.VirtualMachineDisk{})
	if err != nil {
		return fmt.Errorf("find disk: %w", err)
	}
	zonalDisk := d.zonalDisks[diskZone]
	return zonalDisk.SetDiskMetadata(cid, metadata)
}

func (d *MultizoneDisks) SnapshotDisk(cid apiv1.DiskCID, meta apiv1.DiskMeta) (apiv1.SnapshotCID, error) {
	zone, err := d.multizoneFinder.GetZone(context.Background(), cid.AsString(), &vmv1.VirtualMachineDisk{})
	if err != nil {
		return apiv1.SnapshotCID{}, fmt.Errorf("find disk: %w", err)
	}
	zonalDisk := d.zonalDisks[zone]
	return zonalDisk.SnapshotDisk(cid, meta)
}

func (d *MultizoneDisks) DeleteSnapshot(cid apiv1.SnapshotCID) error {
	zone, err := d.multizoneFinder.GetZone(context.Background(), cid.AsString(), &vmv1.VirtualMachineBackup{})
	if err != nil {
		return fmt.Errorf("find backup: %w", err)
	}
	zonalDisk := d.zonalDisks[zone]
	return zonalDisk.DeleteSnapshot(cid)
}

func (d *MultizoneDisks) getCommonZone(vmCID apiv1.VMCID, diskCID apiv1.DiskCID) (string, error) {
	vmZone, err := d.multizoneFinder.GetZone(context.Background(), vmCID.AsString(), &vmv1.VirtualMachine{})
	if err != nil {
		return "", fmt.Errorf("find vm: %w", err)
	}
	diskZone, err := d.multizoneFinder.GetZone(context.Background(), diskCID.AsString(), &vmv1.VirtualMachineDisk{})
	if err != nil {
		return "", fmt.Errorf("find disk: %w", err)
	}
	if vmZone != diskZone {
		return "", fmt.Errorf("VM is in zone %q, but the disk is in zone %q", vmZone, diskZone)
	}
	return vmZone, nil
}

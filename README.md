# bosh-gdc-cpi-release

## Overview

`bosh-gdc-cpi-release` is the [Cloud Foundry BOSH](https://bosh.io/docs/) [Cloud Provider Interface (CPI)](https://bosh.io/docs/cpi-api-v2/) release for **Google Distributed Cloud air-gapped (GDC)**.

A BOSH CPI translates abstract infrastructure operations requested by the BOSH Director into cloud-specific API calls. This release enables BOSH to deploy, scale, and manage Cloud Foundry and other BOSH-managed workloads natively on GDC infrastructure across single-zone and multi-zone environments.

## Functional Capabilities

`bosh-gdc-cpi-release` implements the BOSH CPI v2 contract against GDC APIs and provides the following core capabilities:

- **Virtual Machine Lifecycle Management**
  - Creates, configures, reboots, and deletes GDC virtual machines (`create_vm`, `delete_vm`, `reboot_vm`, `has_vm`, `set_vm_metadata`, `calculate_vm_cloud_properties`).
  - Configures machine types or custom CPU/memory resources, boot disks, and ephemeral disks.
  - Bootstraps and updates the BOSH Agent environment on target VMs using the GDC VM Metadata Server or SSH access requests (`VirtualMachineAccessRequest`).

- **Persistent Disk Management**
  - Creates, resizes, snapshots, and deletes persistent disks on GDC storage (`create_disk`, `delete_disk`, `resize_disk`, `has_disk`, `set_disk_metadata`, `snapshot_disk`, `delete_snapshot`).
  - Attaches and detaches persistent disks to and from VMs (`attach_disk`, `detach_disk`, `get_disks`), including support for disk hotplugging and BOSH Agent disk hint synchronization.

- **Stemcell & OS Image Management**
  - Supports importing BOSH stemcell archives into GDC by uploading stemcell disk images to GDC object storage and provisioning `VirtualMachineImage` resources across configured zones (`create_stemcell`, `delete_stemcell`).
  - Supports referencing pre-imported GDC virtual machine images (light stemcells).

- **Networking & Load Balancing**
  - Configures dynamic and manual VM network interfaces, security groups/network policies, and Tier-1 networking on GDC.
  - Manages external IP connectivity and load balancer integration via GDC networking resources (`LoadBalancer` and `CloudNATGateway`).

- **Multi-Zone & Authentication Support**
  - Authenticates against GDC global and zonal API servers using GDC Service Identity credentials (JWT signing and Security Token Service token exchange) and custom CA trust bundles.
  - Routes VM, disk, and stemcell operations across multiple GDC availability zones.

## Community & Governance

This repository is maintained under the Cloud Foundry [Foundational Infrastructure Working Group](https://github.com/cloudfoundry/community/blob/main/toc/working-groups/foundational-infrastructure.md) (`bosh-gdc-cpi` area).

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to file issues and submit pull requests.

## Security

Please see [SECURITY.md](SECURITY.md) for instructions on reporting security vulnerabilities.

## License

`bosh-gdc-cpi-release` is licensed under the [Apache 2.0 License](LICENSE).

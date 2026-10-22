# bosh-gdc-cpi-release

## Overview

`bosh-gdc-cpi-release` is the [Cloud Foundry BOSH](https://bosh.io/docs/) [Cloud Provider Interface (CPI)](https://bosh.io/docs/cpi-api-v2/) release for **Google Distributed Cloud air-gapped (GDC)**.

A BOSH CPI translates abstract infrastructure operations requested by the BOSH Director into cloud-specific API calls. This release enables BOSH to deploy, scale, and manage Cloud Foundry and other BOSH-managed workloads natively on GDC infrastructure across single-zone and multi-zone environments.

For background on creating BOSH releases, see [Create a Release](https://bosh.io/docs/create-release/) from `bosh.io`.

## Functional Capabilities

`bosh-gdc-cpi-release` implements the BOSH CPI v2 contract against GDC APIs and provides the following core capabilities:

- **Virtual Machine Lifecycle Management**
  - Creates, configures, reboots, and deletes GDC virtual machines (`create_vm`, `delete_vm`, `reboot_vm`, `has_vm`, `set_vm_metadata`, `calculate_vm_cloud_properties`).
  - Configures machine types or custom CPU/memory resources, boot disks, and ephemeral disks.
  - Bootstraps and updates the BOSH Agent environment on target VMs using the GDC VM Metadata Server or SSH access requests (`VirtualMachineAccessRequest`).

- **Persistent Disk Management**
  - Creates, resizes, snapshots, and deletes persistent disks on GDC storage (`create_disk`, `delete_disk`, `resize_disk`, `has_disk`, `set_disk_metadata`, `snapshot_disk`, `delete_snapshot`).
  - Attaches and detaches persistent disks to and from VMs (`attach_disk`, `detach_disk`, `get_disks`), including support for disk hotplugging, hot disk expansion, and BOSH Agent disk hint synchronization.

- **Stemcell & OS Image Management**
  - Supports importing BOSH stemcell archives into GDC by uploading stemcell disk images to GDC object storage and provisioning `VirtualMachineImage` resources across configured zones (`create_stemcell`, `delete_stemcell`).
  - Supports referencing pre-imported GDC virtual machine images (light stemcells).

- **Networking & Load Balancing**
  - Configures dynamic and manual VM network interfaces, security groups/network policies, and Tier-1 networking on GDC.
  - Manages external IP connectivity and load balancer integration via GDC networking resources (`LoadBalancer` and `CloudNATGateway`).

- **Multi-Zone & Authentication Support**
  - Authenticates against GDC global and zonal API servers using GDC Service Identity credentials (JWT signing and Security Token Service token exchange) and custom CA trust bundles.
  - Routes VM, disk, and stemcell operations across multiple GDC availability zones.

## Repository Structure

```text
.
├── .github/workflows  # GitHub Actions CI and presubmit integration test workflows
├── config             # Configuration of the release (blobs, final release specs)
├── jobs               # BOSH job definitions (gdc_cpi)
├── packages           # BOSH package definitions (bosh-gdc-cpi)
├── bosh-cpi           # Go source code for the CPI implementation and tests
│   ├── cmd            # Main CPI executable entrypoint
│   ├── pkg            # CPI packages (vm, disk, stemcell, auth, client, gdc, etc.)
│   ├── tools          # CPI configuration file generator (gen_config_file.sh)
│   └── integration    # Integration test suite against live GDC environments
├── scripts            # Build and CI helper scripts
└── Makefile           # Standard developer build, lint, test, and release targets
```

## Developer Guide

Standard developer lifecycle commands are provided via `make`:

### 1. Build Binaries

To compile CPI binaries for both Linux and Darwin:

```bash
make build
```

Compiled binaries will be placed in `bin/cpi` (Linux amd64) and `bin/cpi_darwin` (Darwin amd64).

You can also compile for a single target:

```bash
make build-linux   # Builds bin/cpi
make build-darwin  # Builds bin/cpi_darwin
```

### 2. Run Unit Tests

Run all unit tests across all packages:

```bash
make test
```

To run unit tests with race detection:

```bash
make test-race
```

### 3. Code Formatting & Linting

Run code formatting (`goimports`, `shfmt`, trailing whitespace cleanup), `go vet`, `golangci-lint`, and `go mod tidy`:

```bash
make pre-check
```

To run formatting or linting individually:

```bash
make format
make check
```

### 4. Create BOSH Release Tarball

Ensure the [BOSH CLI](https://bosh.io/docs/cli-v2-install/) is installed.

To compile CPI binaries and generate the BOSH release tarball (`bin/bosh_gdc_cpi_release_<VERSION>.tgz`):

```bash
make release
```

Alternatively, prepare the binaries in `src/bosh-gdc-cpi/` and run `bosh create-release` manually:

```bash
./scripts/prepare_cpi_binary.sh
bosh create-release --force --tarball=<tarball_destination_path>
```

### 5. Vulnerability Scanning

Run `govulncheck` on the compiled Linux CPI binary:

```bash
make vuln-scan
```

### 6. Clean Artifacts

Clean built binaries, temporary packaging directories, and test artifacts:

```bash
make clean
```

## Integration Testing & Presubmit CI

To run integration tests locally against a target GDC environment:

```bash
make integration-test TEST_ARGS="--configPath=<path_to_config.json>"
```

See [bosh-cpi/README.md](bosh-cpi/README.md) and [bosh-cpi/tools/README.md](bosh-cpi/tools/README.md) for detailed instructions on generating a CPI configuration file and configuring a GDC test project.

### GitHub Actions Presubmit Checks

Pull requests targeting `main` automatically run:

1. **CI (`.github/workflows/ci.yaml`)**:
   - `Check (Lint & Format)`: Verifies `make check`, formatting (`make format`), and module tidiness (`make tidy`).
   - `Unit Tests`: Runs `make test`.
   - `Build Binaries`: Compiles Linux/Darwin CPI binaries (`make build`) and release packaging binaries (`make release-binaries`).

2. **Presubmit Integration Test (`.github/workflows/integration-test.yaml`)**:
   - Runs live multizone integration tests against the `cfbosh-staging` GitHub environment.
   - Automatically selects the relevant integration test suite based on modified paths (`bosh-cpi/pkg/disk/*` -> `TestMultizoneDiskMethods`, `bosh-cpi/pkg/vm/*` -> `TestMultizoneVMAndDiskMethods`, `bosh-cpi/pkg/stemcell/*` -> `TestMultizoneStemcellMethods`, or `TestMultizone` when multiple packages or shared files change).
   - Supports commit message directives (`[skip-cpi-integ]` and `[cpi-integ-filter: <FILTER>]`), PR comments (`/test-integration [FILTER]`), and manual `workflow_dispatch` runs.

## Community & Governance

This repository is maintained under the Cloud Foundry [Foundational Infrastructure Working Group](https://github.com/cloudfoundry/community/blob/main/toc/working-groups/foundational-infrastructure.md) (`bosh-gdc-cpi` area).

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to file issues and submit pull requests.

## Security

Please see [SECURITY.md](SECURITY.md) for instructions on reporting security vulnerabilities.

## License

`bosh-gdc-cpi-release` is licensed under the [Apache 2.0 License](LICENSE).

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
├── .github/workflows  # GitHub Actions CI, integration test, release pipeline, and dashboard workflows
├── .final_builds      # BOSH final-build indices (job/package/license blobs on GitHub Release assets) used by bosh.io
├── config             # Configuration of the release (blobs, final release specs incl. read-only blobstore)
├── jobs               # BOSH job definitions (gdc_cpi)
├── packages           # BOSH package definitions (bosh-gdc-cpi)
├── releases           # Committed BOSH final-release manifests (bosh-gdc-cpi-<VERSION>.yml, index.yml) used by bosh.io
├── bosh-cpi           # Go source code for the CPI implementation and tests
│   ├── cmd            # Main CPI executable entrypoint
│   ├── pkg            # CPI packages (vm, disk, stemcell, auth, client, gdc, etc.)
│   ├── tools          # CPI configuration file generator (gen_config_file.sh)
│   └── integration    # Integration test suite against live GDC environments
├── ci                 # Concourse deployment pipeline (ci/pipeline/, ci/configure.sh, ci/run-pipeline.sh)
├── dashboard          # Release pipeline CI/CD dashboard (GitHub Pages)
├── scripts            # Build, SemVer release, and CI helper scripts
├── VERSION            # Current development SemVer target (e.g. v0.1.0-dev)
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

### 4. Create BOSH Release Tarball (SemVer)

Ensure the [BOSH CLI](https://bosh.io/docs/cli-v2-install/) is installed.

To compile CPI binaries, create a versioned BOSH release tarball (`releases/bosh-gdc-cpi/bosh-gdc-cpi-<VERSION>.tgz`), verify `release.MF` identity via `scripts/_release_artifact.py`, and generate its `.sha256` checksum:

```bash
make release VERSION=0.1.0
```

To build a timestamped development tarball (`dev_releases/bosh-gdc-cpi/bosh-gdc-cpi-dev-<timestamp>.tgz`):

```bash
make dev-release
```

### 5. Vulnerability Scanning

Run `govulncheck` on the compiled Linux CPI binary and render the report:

```bash
make vuln-scan                          # fails if any finding has a published fix
make vuln-scan VULN_FAIL_ON_FIXABLE=false   # report only
```

**How the scan works.** `govulncheck -mode=binary` performs a *symbol-level* scan of the linked binary against the Go vulnerability database (https://vuln.go.dev): a finding means the vulnerable function is linked into `cpi_binary_linux` (dead code is already eliminated by the linker), not that the CPI necessarily calls it. [`scripts/_vuln_report.py`](scripts/_vuln_report.py) turns the JSON output into `vulnerability-report-<VERSION>.md`, grouped per module with the **linked version**, the **fixed-in version**, CVE/GHSA aliases and a summary, split into two sections:

- **Fixable — upgrade required**: the database records a fixed version. `stdlib` findings are fixed by bumping the Go toolchain (the exact patch version in the `go` directive of [`go.mod`](go.mod), which `actions/setup-go` and the Makefile's hermetic toolchain both honour); other modules by `go get <module>@<fixed>`.
- **No upstream fix available**: advisories without a fixed version (end-of-life or unmaintained packages pulled in transitively), listed for transparency and tracked as dependency-roadmap items.

**Release gate.** Stage 2 of the release pipeline runs the same scan via `scripts/release-cpi.sh --stage candidate` with `VULN_FAIL_ON_FIXABLE=true` (default): a candidate whose binary carries a vulnerability with a published fix **fails the build** with the required upgrades printed (`upgrade: stdlib -> v1.26.9, golang.org/x/net -> v0.62.0, …`). Use `--allow-fixable-vulns` / `VULN_FAIL_ON_FIXABLE=false` only as a conscious, temporary override. Findings without an upstream fix never block a release but appear in the report attached to the GitHub Release and uploaded to `gs://…/vulnerability-reports/cpi/<VERSION>.md`.

> **Keep the toolchain current.** Most historical findings were Go standard-library CVEs caused by building with an outdated patch release. The `go` directive in `go.mod` pins the exact toolchain (e.g. `go 1.26.10`); bump it (and `GO_VERSION` in the [`Makefile`](Makefile)) whenever a new Go patch release ships.

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

## Release Pipeline, Artifacts & CI/CD Dashboard

### CFBOSH GDC Release Pipeline (`.github/workflows/release-pipeline.yaml`)

The **`CFBOSH GDC release pipeline`** automates end-to-end BOSH CPI release packaging, Concourse deployment verification, vulnerability scanning, and SemVer release publishing.

- **Execution Modes (`snapshot` vs `release`)**:
  - **`snapshot` mode (`vX.Y.Z-dev.<sha>`)**: Builds the candidate CPI tarball, runs `govulncheck`, uploads candidate artifacts to the internal CI/CD GCS bucket (`gs://cfbosh-cicd/cpi_releases/`), and runs the Concourse deployment pipeline on GKE. **Does not create a GitHub Release, git tag, or external GCS upload (`gs://gdc-external-sap/`).** The pipeline only runs merged code on `main`: there are no Pull Request triggers, and `workflow_dispatch` from any other branch fails. `scripts/release-cpi.sh` independently refuses `--mode release` unless the commit is on `main` (or the matching `release/X.Y` branch), verified by Git ancestry.
  - **`release` mode (`vX.Y.Z`)**: Only allowed on `main` (via the biweekly schedule or manual `workflow_dispatch` on `main`), and always runs the Concourse stage (`skip_concourse` is rejected in `release` mode). Runs the full pipeline and publishes the official SemVer GitHub Release (`vX.Y.Z` tag) and external GCS artifacts (`gs://gdc-external-sap/cpi_releases/`).
- **Who can trigger it**: manual runs (`workflow_dispatch` on `main`) are accepted only from users listed in [`OWNERS_ALIASES`](OWNERS_ALIASES) / [`CODEOWNERS`](CODEOWNERS) **as committed on `main`** (least privilege; organization or collaborator status alone is not sufficient). The Concourse stage runs on an internal, maintainer-operated Concourse instance deploying to the GDC staging environment, so it is not available to external contributors; the `CI` and `Presubmit Integration Test` workflows are the Pull Request checks. Changes reach Concourse only after they are merged to `main`.
- **How to Trigger the Release Pipeline**:
  1. **Automated Biweekly Schedule (`schedule`)**:
     - Runs automatically on the 1st and 15th of each month at `00:00 UTC` (`0 0 1,15 * *`), in `release` mode on `main` (biweekly release cadence).
  2. **Manual Trigger from GitHub Actions UI (`workflow_dispatch`)**:
     - Navigate to [**Actions &rarr; CFBOSH GDC release pipeline &rarr; Run workflow**](https://github.com/cloudfoundry/bosh-gdc-cpi-release/actions/workflows/release-pipeline.yaml) and configure the following inputs:
       | Input | Options / Type | Default | Description |
       | :--- | :--- | :--- | :--- |
       | `release_mode` | `snapshot` \| `release` | `snapshot` | Execution mode (`snapshot` for internal GCS/Concourse validation; `release` for official SemVer GitHub & GCS release on `main`). Must be run from `main`. |
       | `version` | `string` (optional) | Derived from Git tags | Explicit SemVer version override (e.g., `0.1.0`; defaults to next SemVer derived from latest `vX.Y.Z` Git tag). |
       | `next_version` | `bump-patch` \| `bump-minor` \| `bump-major` \| `noop` | `bump-patch` | SemVer increment strategy applied to the latest `vX.Y.Z` Git tag to compute the target release version (defaults to `0.1.0` if no final tags exist yet). |
       | `skip_concourse` | `boolean` (`true` \| `false`) | `false` | **Snapshot mode only.** Skip Stage 3 (`Concourse`) in case of underlying infrastructure issues to validate the build/publish path; the run fails fast if combined with `release_mode=release`. |
       | `concourse_wait_for` | `promote-candidate` \| `bosh-deployment` | `promote-candidate` | Target Concourse job to monitor before proceeding to Stage 4 (`promote-candidate` waits for the full Concourse pipeline). |
- **Pipeline Stages**:
  1. **Authorize & Resolve SemVer**: Verifies maintainer authorization and derives the target SemVer version/tag (`vX.Y.Z` in `release` mode or `vX.Y.Z-dev.<sha>` in `snapshot` mode) directly from repository Git tags via `scripts/_release_tag.py` (with Stage `1b` immediately publishing a live `running` entry to the dashboard).
  2. **Build Candidate CPI & Scan Vulnerabilities**: Compiles Linux and Darwin CPI binaries (`make release-binaries`), creates the BOSH CPI release archive (`scripts/create-release`), verifies `release.MF` identity (`name: bosh-gdc-cpi` and `version`) and SHA-256 digest via `scripts/_release_artifact.py`, runs `govulncheck` against the Linux CPI binary (failing the build if any finding has a published fix — see [Vulnerability Scanning](#5-vulnerability-scanning)), and uploads candidate artifacts to `gs://cfbosh-cicd/`.
  3. **Run Concourse Deployment Pipeline (GKE)**: Connects to the GKE Concourse cluster (`cfbosh`), renders and updates the [`cfbosh-deployment`](https://cfbosh-concourse-web.corp.goog/teams/main/pipelines/cfbosh-deployment) pipeline (`ci/configure.sh`), checks the `bosh-cpi-releases` GCS resource, and triggers/watches the Concourse pipeline (`start-deployments` &rarr; `bosh-bootstrap` &rarr; `bosh-deployment` &rarr; `run-bats` / `cf-deployment` &rarr; `run-cats` &rarr; `pre-release-fan-in` &rarr; `promote-candidate`) via `ci/run-pipeline.sh`. Can be bypassed in `snapshot` mode by setting `skip_concourse: true` in case of underlying infrastructure issues.
  4. **Release CPI Artifact (GitHub & GCS)**: In `release` mode (on `main`), publishes the verified CPI release artifacts and vulnerability report to GitHub Releases (creating the `vX.Y.Z` tag) and the external GCS bucket (`gs://gdc-external-sap/`). In `snapshot` mode (including all PR runs), skips external GitHub Release creation, git tagging, and external GCS promotion.
  5. **Update Release Pipeline Dashboard**: Appends the run telemetry and stage statuses to `dashboard/data/runs.json`, generates the GitHub Actions job summary, and deploys the updated dashboard to GitHub Pages.

#### Monitoring & Debugging Concourse Pipeline Runs

When Stage 3 (`3. Run Concourse Deployment Pipeline (GKE)`) is running or fails, maintainers on the Google corporate network can inspect real-time task logs, resource checks, and step outputs directly in the **Concourse Web UI**:

- **Concourse Pipeline Overview**: [`https://cfbosh-concourse-web.corp.goog/teams/main/pipelines/cfbosh-deployment`](https://cfbosh-concourse-web.corp.goog/teams/main/pipelines/cfbosh-deployment)
- **Direct Job Build URL Pattern**:
  ```text
  https://cfbosh-concourse-web.corp.goog/teams/main/pipelines/cfbosh-deployment/jobs/<job-name>/builds/<build-number>
  ```
  For example, to inspect or debug build `#88` of the `bosh-deployment` job:
  - [`https://cfbosh-concourse-web.corp.goog/teams/main/pipelines/cfbosh-deployment/jobs/bosh-deployment/builds/88`](https://cfbosh-concourse-web.corp.goog/teams/main/pipelines/cfbosh-deployment/jobs/bosh-deployment/builds/88)
- **Locating the Build Number & Logs**:
  - The GitHub Actions log for Stage 3 (`Deploy & Execute Concourse Pipeline on GKE`) prints the active Concourse job and build number as it progresses (e.g., `bosh-deployment #88`) and streams the task output on failure.
  - Each run also records its Concourse execution log in GCS (`gs://cfbosh-cicd/cicd-deployment-logs/log_<TIMESTAMP>.md`), which is linked in the expandable run details on the [Release Dashboard](https://cloudfoundry.github.io/bosh-gdc-cpi-release/).
  - To connect via `fly` CLI or `fly intercept` into a running/failed Concourse worker container from your workstation, follow [Connect to the prod deployment pipeline in `ci/README.md`](ci/README.md#connect-to-the-prod-deployment-pipeline).

### SemVer Version Management & Release Design Approach

The release versioning and artifact verification architecture follows [Semantic Versioning 2.0.0](https://semver.org/) and standard Cloud Foundry BOSH CPI conventions (no static `VERSION` file tracked on `main`):

1. **Git-Tag-Driven Version Derivation (`scripts/_release_tag.py` & `Makefile`)**:
   - Rather than maintaining a static `VERSION` file on `main`, versions are derived dynamically from **Git tags (`vX.Y.Z`)** and BOSH release metadata ([`config/final.yml`](config/final.yml)):
     - In [`Makefile`](Makefile), local builds derive `VERSION` via `git describe --tags --always --dirty`.
     - In CI (`scripts/release-cpi.sh` &rarr; `scripts/_release_tag.py --resolve-next <mode>`), the pipeline inspects all final `vX.Y.Z` Git tags in the repository:
       - If no final release tag exists yet, the initial base version defaults to `0.1.0` (or `1.0.0` when `bump-major` is selected).
       - Once a final tag (such as `v0.1.0`) exists, the next target version is computed automatically from the highest final `vX.Y.Z` tag according to `next_version` (`bump-patch` &rarr; `0.1.1`, `bump-minor` &rarr; `0.2.0`, `bump-major` &rarr; `1.0.0`), unless an explicit `version` override is supplied.
   - **In `snapshot` mode** (used for all Pull Request runs and pre-release validations): the pipeline appends `-dev.<short_sha>` to the resolved base version (e.g., `0.1.0-dev.d426745`, tag `v0.1.0-dev.d426745`), producing a unique, traceable SemVer 2.0 pre-release version tied to the exact tested Git commit without creating any Git tags or external releases.
   - **In `release` mode** (used for biweekly scheduled releases and manual releases on `main`): the pipeline uses the final SemVer version `X.Y.Z` and creates the annotated Git tag `vX.Y.Z` when publishing the GitHub Release, which immediately becomes the baseline for subsequent runs without needing any post-release version-bump commits on `main`.

2. **Tag & Branch Lineage Enforcement (`scripts/_release_tag.py`)**:
   - Before building or publishing, [`scripts/_release_tag.py`](scripts/_release_tag.py) validates the version tag and commit lineage against the repository's branches and tag history (covered by unit tests in [`scripts/_release_tag_test.py`](scripts/_release_tag_test.py)):
     - **Strict SemVer 2.0 Grammar**: Enforces `v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.]+))?`, rejecting leading zeros or malformed tags.
     - **Branch Lineage Guardrails**:
       - Every `.0` release (`vX.Y.0`) and pre-release on a line without a maintenance branch must point to a commit on `main`.
       - Any patch release (`vX.Y.Z` with `Z > 0`) must come from `release/X.Y` whenever that release branch exists (preventing newer `main` work from shipping under an older minor release line), or from `main` if no `release/X.Y` branch has been cut.
       - Non-`main` branches and Pull Requests are only permitted when `--allow-branch` is passed in `snapshot` mode.
     - **`Latest` Release Badge Resolution**: Marks a GitHub Release as `Latest` if and only if the tag has no pre-release suffix and its `(major, minor, patch)` tuple is greater than or equal to every existing final release tag in the repository. A backported patch on an older `release/X.Y` branch never steals the `Latest` badge from a newer minor or major release.

3. **Deterministic Build & Archive Integrity Verification (`scripts/create-release` & `scripts/_release_artifact.py`)**:
   - **Single Source of Truth for Release Name**: Both [`Makefile`](Makefile) (`make release-name-check`) and [`scripts/create-release`](scripts/create-release) read `name: bosh-gdc-cpi` directly from [`config/final.yml`](config/final.yml) so the archive filename, directory path (`dev_releases/bosh-gdc-cpi/` and `releases/bosh-gdc-cpi/`), and embedded `release.MF` cannot drift.
   - **Compile-Time Version Injection**: [`Makefile`](Makefile) compiles both Linux (`cpi_binary_linux`) and Darwin (`cpi_binary_darwin`) CPI binaries with `-ldflags "-X main.Version=$(VERSION)"`.
   - **BOSH Release Packaging**: [`scripts/create-release`](scripts/create-release) stages the compiled binaries, runs `bosh create-release --force --version "<VERSION>" --tarball "releases/bosh-gdc-cpi/bosh-gdc-cpi-<VERSION>.tgz"`, and writes the companion `bosh-gdc-cpi-<VERSION>.tgz.sha256` checksum file.
   - **Manifest & Digest Inspection**: [`scripts/_release_artifact.py`](scripts/_release_artifact.py) (tested in [`scripts/_release_artifact_test.py`](scripts/_release_artifact_test.py)) inspects the `.tgz` archive in both Stage 2 (build) and Stage 4 (publish) to verify that:
     - `release.MF` is present inside the archive.
     - `release.MF` declares `name: bosh-gdc-cpi` and `version: "<VERSION>"` matching the expected SemVer string.
     - The archive's SHA-256 digest matches the `.sha256` checksum built in Stage 2, guaranteeing the published `bosh-gdc-cpi-<VERSION>.tgz` is byte-identical to the candidate build. (Concourse qualifies the `bosh_gdch_cpi_release_<VERSION>.tgz` compatibility build, which contains the same CPI binary under the legacy release/job names.)

### Artifacts Produced by the Release Pipeline

Each run of the `CFBOSH GDC release pipeline` produces and verifies the following CPI release artifacts, and validates them in Concourse against the latest upstream GDC Ubuntu Noble stemcell (`bosh-stemcell-<version>-gdch-kvm-ubuntu-noble.tgz`, git tag `ubuntu-noble/v<version>`) published by [`cloudfoundry/bosh-linux-stemcell-builder`](https://github.com/cloudfoundry/bosh-linux-stemcell-builder) (`ubuntu-noble` branch, `gs://bosh-core-stemcells-candidate/gdch/`):

| Artifact | Location / Destination | Description |
| :--- | :--- | :--- |
| **`bosh-gdc-cpi-<VERSION>.tgz`** | [GitHub Releases](https://github.com/cloudfoundry/bosh-gdc-cpi-release/releases),<br>`gs://cfbosh-cicd/cpi_releases/`,<br>`gs://gdc-external-sap/cpi_releases/` | Official SemVer BOSH CPI release tarball containing the `gdc_cpi` job, `bosh-gdc-cpi` package (Linux & Darwin binaries), and `release.MF` manifest. |
| **`bosh-gdc-cpi-<VERSION>.tgz.sha256`** | [GitHub Releases](https://github.com/cloudfoundry/bosh-gdc-cpi-release/releases),<br>`gs://cfbosh-cicd/cpi_releases/`,<br>`gs://gdc-external-sap/cpi_releases/` | SHA-256 checksum file (`<sha256>  bosh-gdc-cpi-<VERSION>.tgz`) verified by `scripts/_release_artifact.py`. |
| **`bosh_gdch_cpi_release_<VERSION>.tgz`** | [GitHub Releases](https://github.com/cloudfoundry/bosh-gdc-cpi-release/releases),<br>`gs://cfbosh-cicd/cpi_releases/`,<br>`gs://gdc-external-sap/cpi_releases/` | Same CPI binary packaged under the **legacy BOSH release/job names** (`bosh-gdch-cpi` / `gdch_cpi`) for existing manifests that reference those names, and consumed by the Concourse `bosh-cpi-releases` GCS resource (`cpi_releases/bosh_gdch_cpi_release_(.*).tgz`). BOSH matches on the release/job *names* inside the tarball, not the file name: **new deployments should use `bosh-gdc-cpi-<VERSION>.tgz`** (`bosh-gdc-cpi` / `gdc_cpi`). |
| **`vulnerability-report-<VERSION>.md`** | [GitHub Releases](https://github.com/cloudfoundry/bosh-gdc-cpi-release/releases),<br>`gs://cfbosh-cicd/vulnerability-reports/cpi/<VERSION>.md`,<br>`gs://gdc-external-sap/vulnerability-reports/cpi/<VERSION>.md` | Markdown vulnerability scan report (`govulncheck -mode=binary` on the Linux CPI binary, rendered by `scripts/_vuln_report.py`): per-module findings with linked/fixed versions and CVE aliases, split into *fixable* (blocks the release) and *no upstream fix*. See [Vulnerability Scanning](#5-vulnerability-scanning). |
| **`job-gdc_cpi-<fingerprint>.tgz`**,<br>**`package-bosh-gdc-cpi-<fingerprint>.tgz`**,<br>**`license-<fingerprint>.tgz`** | [GitHub Releases](https://github.com/cloudfoundry/bosh-gdc-cpi-release/releases) (`v<VERSION>` assets) | BOSH final-build blobs referenced by the committed `.final_builds/**/index.yml` (`blobstore_id: v<VERSION>/<asset>`). They make the release rebuildable by [bosh.io](https://bosh.io/releases) from metadata alone (see below). |
| **`releases/bosh-gdc-cpi/bosh-gdc-cpi-<VERSION>.yml`**,<br>**`releases/bosh-gdc-cpi/index.yml`**,<br>**`.final_builds/**/index.yml`** | Committed to `main` (`chore(release): cut bosh-gdc-cpi v<VERSION>`) | BOSH final-release metadata generated by `bosh finalize-release` ([`scripts/_final_release.py`](scripts/_final_release.py)). |
| **Concourse Deployment Logs** | `gs://cfbosh-cicd/cicd-deployment-logs/log_<TIMESTAMP>.md` | Execution log and build ID tracking artifact generated by the Concourse `cfbosh-deployment` pipeline. |

#### bosh.io Final-Release Metadata (`releases/` and `.final_builds/`)

[bosh.io](https://bosh.io/releases) ([`cloudfoundry/bosh-io-releases`](https://github.com/cloudfoundry/bosh-io-releases)) does **not** look at GitHub Releases. It watches the `releases/` directory on the default branch and rebuilds every final release with `bosh create-release releases/bosh-gdc-cpi/bosh-gdc-cpi-<VERSION>.yml`, downloading each job/package/license blob listed in `.final_builds/**/index.yml` from the blobstore declared in [`config/final.yml`](config/final.yml). This repository therefore:

- Declares a **read-only, anonymous blobstore** in [`config/final.yml`](config/final.yml) (bosh CLI `s3` provider, `credentials_source: none`) that resolves `blobstore_id: v<VERSION>/<asset>` to the public GitHub Release asset `https://github.com/cloudfoundry/bosh-gdc-cpi-release/releases/download/v<VERSION>/<asset>`. No bucket or credentials are required to rebuild a release.
- In `release` mode, Stage 4 (`scripts/release-cpi.sh --stage publish`) runs [`scripts/_final_release.py finalize`](scripts/_final_release.py) on the verified tarball (`bosh finalize-release` against a throwaway local blobstore, then rewrites the fresh `blobstore_id`s to `v<VERSION>/<kind>-<name>-<fingerprint>.tgz`), uploads those blobs as assets of the `v<VERSION>` GitHub Release, runs [`scripts/_final_release.py verify`](scripts/_final_release.py) (a pristine `bosh create-release` from the manifest with an empty blob cache, exactly what bosh.io does), and finally commits `releases/` + `.final_builds/` to `main` as `chore(release): cut bosh-gdc-cpi v<VERSION>`.
- Keeps `releases/**/*.yml` and `.final_builds/` tracked in git (only the built `*.tgz` / `*.sha256` / reports next to them are ignored); `make release-clean` never deletes them.

To check a published version locally from a fresh clone (no credentials, no `src/` binaries needed):

```bash
bosh create-release releases/bosh-gdc-cpi/bosh-gdc-cpi-<VERSION>.yml --tarball /tmp/bosh-gdc-cpi-<VERSION>.tgz
# or equivalently, with an empty blob cache:
python3 scripts/_final_release.py verify --version <VERSION> --tarball /tmp/bosh-gdc-cpi-<VERSION>.tgz
```

Every GitHub Release also includes a ready-to-paste **BOSH deployment manifest snippet** in the release notes, followed by an auto-generated **What's Changed** section (see [Release Notes](#release-notes) below):

```yaml
releases:
  - name: bosh-gdc-cpi
    version: "<VERSION>"
    url: https://github.com/cloudfoundry/bosh-gdc-cpi-release/releases/download/v<VERSION>/bosh-gdc-cpi-<VERSION>.tgz
    sha1: sha256:<SHA256>
```

### Release Notes

Release notes are **generated by the pipeline, not written by hand**. Every `vX.Y.Z` GitHub Release body is assembled by `scripts/release-cpi.sh --stage publish` from three parts, in this order:

1. **BOSH deployment manifest snippet** (above) with the release URL and SHA-256 digest.
2. **Release Provenance & Verification** table (release name, version, tag, commit SHA, SHA-256 digest, Concourse stage result, GCS release bucket) followed by the `govulncheck` **vulnerability report** for the shipped binary.
3. **What's Changed** — GitHub's [automatically generated release notes](https://docs.github.com/en/repositories/releasing-projects-on-github/automatically-generated-release-notes), fetched through the `POST /repos/{owner}/{repo}/releases/generate-notes` API so it works both when the release is first created and when an existing release is updated on a pipeline re-run. It lists every Pull Request merged since the previous final tag (one bullet per PR: title, author, PR link), a **New Contributors** section, and a **Full Changelog** compare link (`v<PREVIOUS>...v<VERSION>`). The previous tag is the highest final `vX.Y.Z` tag already resolved in Stage 1 (`scripts/_release_tag.py --check-new-commits`), so the changelog always spans exactly the commits that are new in this release. If note generation fails (for example, an API outage), the pipeline logs a warning and publishes the release without this section rather than failing the release.

PRs are grouped into sections by their **labels**, as configured in [`.github/release.yml`](.github/release.yml):

| Section | PR labels |
| :--- | :--- |
| 🚀 Features | `enhancement`, `feature` |
| 🐛 Bug Fixes | `bug`, `fix` |
| 🔒 Security | `security` |
| 🧪 CI / Release Pipeline | `ci`, `release-pipeline` |
| 📚 Documentation | `documentation` |
| ⬆️ Dependencies | `dependencies` |
| Other Changes | any PR without one of the labels above |

PRs labelled `skip-changelog` or `no-release-notes`, and PRs authored by `github-actions[bot]` / `dependabot[bot]`, are excluded.

#### What this means for authors

Because the PR list *is* the changelog, the quality of the release notes depends on the Pull Requests that land on `main`:

- **Write the PR title as the changelog line.** Use the [Conventional Commits](https://www.conventionalcommits.org/) form already used in this repository (`feat(vm): …`, `fix(disk): …`, `ci(github): …`, `docs: …`) and describe the user-visible outcome, not the implementation (`fix(disk): retry snapshot when backup is still provisioning`, not `fix bug`). The title is copied verbatim into the release.
- **Label the PR** with one of the labels above so it lands in the right section; unlabelled PRs fall under *Other Changes*. Use `skip-changelog` for pure noise (typo fixes, dashboard data refreshes, revert-of-unreleased-change) that should not appear in the notes.
- **Merge via Pull Request.** Direct pushes to `main` are not PRs and therefore never appear in *What's Changed* (they are still covered by the *Full Changelog* compare link). The only expected direct commits are the automated `chore(release): cut bosh-gdc-cpi vX.Y.Z` metadata commits.
- **Call out breaking changes and operator actions in the PR description** (a `BREAKING CHANGE:` paragraph or a short *Upgrade notes* section). After the pipeline publishes the release, a maintainer can add such notes at the top of the GitHub Release body (`gh release edit vX.Y.Z --notes-file …`). Note that re-running the publish stage for the same version regenerates the whole body, so re-apply manual additions afterwards.
- **One logical change per PR.** Squash fix-ups before merging; a PR that mixes a feature and an unrelated refactor produces a misleading changelog entry.

To preview the notes a release would get before cutting it:

```bash
gh api --method POST repos/cloudfoundry/bosh-gdc-cpi-release/releases/generate-notes \
  -f tag_name=v<NEXT> -f target_commitish=main -f previous_tag_name=v<LATEST> --jq '.body'
```

### Release Pipeline CI/CD Dashboard

The execution history, stage breakdown, and live status of all `CFBOSH GDC release pipeline` runs can be viewed on the **CFBOSH GDC CPI Release Dashboard**:

- **Dashboard URL**: **[https://cloudfoundry.github.io/bosh-gdc-cpi-release/](https://cloudfoundry.github.io/bosh-gdc-cpi-release/)**
- **Features**:
  - **KPI Summary Cards**: Displays Total Runs, Success Rate, Average Duration, and Latest CPI Release version (`vX.Y.Z`).
  - **Latest Run Stage Progress Banner**: Visualizes the status (`Passed`, `Running`, `Skipped`, `Failed`) of all 4 pipeline stages (`1. Build & Verify CPI`, `2. Concourse Pipeline`, `3. Vulnerability Scan`, `4. GitHub & GCS Release`).
  - **Execution History Table**: Searchable and filterable history of all past and live in-progress pipeline runs with expandable stage details, SHA-256 digests, GCS artifact paths, and direct links to the GitHub Release and GitHub Actions workflow logs.
  - **Automated Publishing**: Automatically deployed to GitHub Pages via `.github/workflows/deploy-dashboard.yaml`.

## Community & Governance

This repository is maintained under the Cloud Foundry [Foundational Infrastructure Working Group](https://github.com/cloudfoundry/community/blob/main/toc/working-groups/foundational-infrastructure.md) (`bosh-gdc-cpi` area).

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines on how to file issues and submit pull requests.

## Security

Please see [SECURITY.md](SECURITY.md) for instructions on reporting security vulnerabilities.

## License

`bosh-gdc-cpi-release` is licensed under the [Apache 2.0 License](LICENSE).

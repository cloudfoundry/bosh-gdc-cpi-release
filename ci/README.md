# GDC Cloud Foundry / BOSH deployment CI/CD pipeline on Concourse cluster

This directory contains the scripts and manifests used for setting up the GDC Cloud Foundry / BOSH deployment CI/CD pipeline running in the Concourse cluster on GKE. The deployment CI/CD pipeline manages different Concourse jobs, and each job is acting as a "stage" consisting of a sequence of tasks. The start of one job is depending on the success of another dependency job, which forms a flow of deployment pipeline conducting the deployments of the long-running BOSH and Cloud Foundry in the GDCag staging environment. The pipeline is maintained by the GDC Cloud Foundry / BOSH maintainers.

## Prerequisites

The following prerequisites are required:
* Access to the GCP project (`cfbosh-cicd`) and GKE cluster (`cfbosh` in `us-east1`) that host the Concourse deployment
* Developers must install the tools listed below (note: `ci/run-pipeline.sh` automatically installs missing CLI dependencies when running in CI):
    * [helm](https://helm.sh/)
    * [gcloud](https://cloud.google.com/sdk/docs/install) (with `gke-gcloud-auth-plugin`)
    * [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl)
    * [jq](https://jqlang.github.io/jq/download)
    * [yq (v4)](https://github.com/mikefarah/yq)
    * [BOSH cli (`bosh`)](https://bosh.io/docs/cli-v2-install)
    * [fly](https://concourse-ci.org/fly.html)

# Concourse jobs of the deployment pipeline

The job graph is visible in the Concourse UI: pipeline `cfbosh-deployment`, group `all`. See your Concourse operator's documentation for how to reach the Concourse web UI.

## Concourse jobs

The deployment CI/CD pipeline has the following Concourse jobs.

### 1. job: start-deployments

The `start-deployments` job is the entry point of a complete end to end deployment, which starts pulling the Docker image used by worker containers executing the tasks. At the end of the job execution, there is a deployment log uploaded to the GCS bucket folder `gs://cfbosh-cicd/cicd-deployment-logs` recording the build ID. Such build ID is the identification for the following sequential jobs.

### 2. job: bosh-bootstrap

The `bosh-bootstrap` job, depending on the success of the `start-deployments` job, handles the tasks of conducting BOSH bootstrap as well as associated deployment assets pulling. At the end of the job execution associated with certain build ID, the deployment log is updated with BOSH bootstrap context. Also, there are deployment artifacts including BOSH bootstrap context uploaded to the GCS bucket folder `gs://cfbosh-cicd/cicd-deployment-logs/artifacts/bosh_bootstrap`.

### 3. job: bosh-deployment

The `bosh-deployment` job, depending on the success of the `bosh-bootstrap` job, handles the tasks of conducting the long-running BOSH environment creation/update in the GDCag staging environment, as well as associated deployment assets pulling. At the end of the job execution associated with certain build ID, the deployment log is updated with BOSH environment creation/update context. Also, the deployment artifacts including BOSH environment creation/update context are updated to the GCS bucket folder `gs://cfbosh-cicd/cicd-deployment-logs/artifacts/bosh_deploy`.

### 4. job: cf-deployment

The `cf-deployment` job, depending on the success of the `bosh-deployment` job, handles the tasks of conducting the long-running Cloud Foundry runtime creation/update in the GDCag staging environment, as well as associated deployment assets pulling. At the end of the job execution associated with certain build ID, the deployment log is updated with Cloud Foundry runtime creation/update context. Also, the deployment artifacts including Cloud Foundry runtime creation/update context are updated to the GCS bucket folder `gs://cfbosh-cicd/cicd-deployment-logs/artifacts/cf_deploy`.

### 5. job: pre-release-fan-in

The `pre-release-fan-in` job is the release gate. It has no tasks: it triggers only when the same run (build ID) has passed both `run-bats` and `run-cats`. The run's `cicd-deployment-logs-bosh` version flows through both jobs (`run-cats` takes it from `cf-deployment`), so `passed: [run-bats, run-cats]` pins a single run, and the CF-stage inputs come from the same `run-cats` build.

### 6. job: promote-candidate

The `promote-candidate` job, depending on the success of the `pre-release-fan-in` job, concludes a qualified run: it runs only for a build where the same run passed both `run-bats` and `run-cats`. At the end of the job execution associated with a build ID, the deployment log is updated with the release context, and the deployment artifacts including the context of all jobs are uploaded to the GCS bucket folder `gs://cfbosh-cicd/cicd-deployment-logs/artifacts`. In the automated **`CFBOSH GDC release pipeline`** (`.github/workflows/release-pipeline.yaml`), `ci/run-pipeline.sh` waits for `promote-candidate` (`--wait-job promote-candidate`) as the terminal Concourse qualification gate before Stage 4 (`scripts/release-cpi.sh --stage publish`) publishes the qualified CPI release artifacts to GitHub Releases and external GCS (`release` mode) or records the validated snapshot (`snapshot` mode).

### 7. job: report-orphaned-vms

The `report-orphaned-vms` job, depending on the success of the `cf-deployment` job, lists the GDC VMs created by this Director's CPI (label `director=bosh-director`) that the Director no longer tracks (`bosh vms`). Such orphans appear when the Director loses its state; they keep consuming quota and IPs. The job is report-only: it never deletes anything, and it fails when it finds orphans so that they get noticed. It does not gate the release, and it holds `deployment-lock` so in-flight VMs of a deploy or BATs are never reported. VMs younger than 30 minutes are skipped.

### Acceptance test jobs

There are two jobs `run-bats` and `run-cats` for running BOSH acceptance tests (BATS) and Cloud Foundry acceptance tests (CATS) respectively, with the respective prerequisite of running BOSH environment and Cloud Foundry runtime.

Both jobs gate the release: `promote-candidate` only runs after `pre-release-fan-in` sees the same run pass `run-bats` and `run-cats`. BATs take about 2h and CATs (express scope) about 1.5h. They do not block `cf-deployment`, and they can still be re-triggered manually; every run of `run-bats` or `run-cats` has a unique run ID associated with the artifacts it generates.

#### job: run-bats

The `run-bats` job, depending on the success of the `bosh-deployment` job, handles the tasks of conducting the running of BOSH acceptance tests (BATS) against the long-running BOSH environment in the GDCag staging environment. At the end of the job execution associated with certain build ID, the deployment log is updated with BATS running context. Also, the deployment artifacts including the BATS running context are updated to the GCS bucket folder `gs://cfbosh-cicd/cicd-deployment-logs/artifacts/bats`.

#### job: run-cats

The `run-cats` job, depending on the success of the `cf-deployment` job, handles the tasks of conducting the running of Cloud Foundry acceptance tests (CATS) against the long-running Cloud Foundry runtime in the GDCag staging environment. At the end of the job execution associated with certain build ID, the deployment log is updated with CATS running context. Also, the deployment artifacts including the CATS running context are updated to the GCS bucket folder `gs://cfbosh-cicd/cicd-deployment-logs/artifacts/cats`.

## Locks

To prevent undesired concurrent executions of jobs with dependency, the jobs are configured with `serial_groups` using Concourse job tags acting as the thread locks. For instance, with the `deployment-lock` tag, `bosh-deployment` job has to be queued until the `bosh-bootstrap` job associated with the same build ID is complete. There are additional tags `bats-lock` and `cats-lock` configured to prevent running `BATS` while BOSH environment creation/update is ongoing and running `CATS` while Cloud Foundry runtime creation/update is ongoing respectively. `run-bats` also holds `deployment-lock` so it never overlaps `cf-deployment`: both run against the same Director, and BATs fails if the Director has any task in progress. Concourse runs the two jobs one after the other, in either order.

## Groups

Jobs are grouped into UI tabs: `all`, `deploy` (`start-deployments`, `bosh-bootstrap`, `bosh-deployment`, `cf-deployment`), `acceptance-tests` (`run-bats`, `run-cats`) `release` (`pre-release-fan-in`, `promote-candidate`) and `housekeeping` (`report-orphaned-vms`).

# Set up CI/CD pipeline in Concourse cluster

## Automated Execution (`ci/run-pipeline.sh` & GitHub Actions)

In CI/CD (Stage 3 of [`.github/workflows/release-pipeline.yaml`](../.github/workflows/release-pipeline.yaml), **`CFBOSH GDC release pipeline`**), Concourse pipeline configuration, triggering, and monitoring are fully automated via [`ci/run-pipeline.sh`](./run-pipeline.sh):

```bash
./ci/run-pipeline.sh \
  --type prod \
  --fly-target cfbosh-gha \
  --wait-job promote-candidate \
  --cpi-version "${CPI_VERSION}" \
  --status-file /tmp/concourse-status.json
```

What `ci/run-pipeline.sh` handles automatically:
1. **CLI Bootstrapping**: Verifies and installs `gcloud`, `gke-gcloud-auth-plugin`, `kubectl`, `jq`, `yq` (v4), `bosh`, and `fly` if missing on the runner.
2. **Stemcell Builder Checkout**: Checks out [`cloudfoundry/bosh-linux-stemcell-builder`](https://github.com/cloudfoundry/bosh-linux-stemcell-builder) (currently pinned to PR [#778](https://github.com/cloudfoundry/bosh-linux-stemcell-builder/pull/778) via `STEMCELL_BUILDER_PR_REF="pull/778/head"` until merged) alongside the repository when needed.
3. **GCP & GKE Authentication**: Authenticates to `cfbosh-cicd` (with optional `--rotate-key` support for service account key rotation), fetches GKE cluster credentials (`cfbosh` in `us-east1`), and renders/applies the Concourse pipeline manifest via [`ci/configure.sh`](./configure.sh).
4. **Resilient Port-Forwarding & Job Progression Monitoring**: Unpauses the pipeline, triggers `start-deployments`, and monitors active Concourse jobs (`start-deployments` &rarr; `bosh-bootstrap` &rarr; `bosh-deployment` &rarr; `cf-deployment` / `run-bats` / `run-cats` &rarr; `pre-release-fan-in` &rarr; `promote-candidate`). If the `kubectl port-forward` tunnel drops during multi-hour `run-bats` or `run-cats` runs, `ensure_concourse_connection()` automatically re-establishes the tunnel and re-authenticates `fly`. If a job fails transiently, `ci/run-pipeline.sh` automatically retries it once before failing the stage.
5. **Telemetry Export**: Writes structured execution metadata (`--status-file`) consumed by Stage 5 (`scripts/update-dashboard.sh`) to update the live [CI/CD Release Dashboard](https://cloudfoundry.github.io/bosh-gdc-cpi-release/).

Supported flags for `ci/run-pipeline.sh`:
* `--type <prod|poc>`: Target Concourse environment (`prod` &rarr; cluster `cfbosh-concourse-ci`, namespace `cfbosh-concourse`, pipeline `cfbosh-deployment`; `poc` &rarr; cluster `cfbosh-concourse-ci-poc`, namespace `cfbosh-concourse-poc`, pipeline `cfbosh-deployment-playground`).
* `--fly-target <name>`: Local `fly` target alias (default: `cfbosh-ci`).
* `--wait-job <job>`: Terminal Concourse job to watch until completion (default: `promote-candidate`; set to `none` to only configure/trigger without waiting).
* `--cpi-version <semver>`: Candidate CPI release version being qualified.
* `--status-file <path>`: Output path for the JSON status summary.
* `--rotate-key`: Rotate the GCP service account key in `gs://cfbosh-cicd/cicd_sa_keys/cfbosh-cicd-sa/cfbosh-cicd-sa-key.json` before configuring the pipeline.
* `--dry-run`: Render manifests (`manifest.yml` and `configs.yml`) and validate without mutating the live Concourse pipeline.

## Play and Test

GDC Cloud Foundry / BOSH maintainers could set up their own test deployment pipeline for testing purpose during development without worrying about messing up the "prod" deployment pipeline.

1. Set up a test Concourse cluster in test namespace

Set up a test Concourse cluster first, in a test namespace on GKE.

2. Create test pipeline in the test Concourse cluster

After setting up the environment variables below:

```
export CONCOURSE_CLUSTER_NAME=<TEST_CONCOURSE_CLUSTER_NAME>
export GKE_CLUSTER_NS=<TEST_NAMESPACE>
export FLY_TARGET=<FLY_TARGET> # e.g. <USER>-test
export PIPELINE_NAME=<TEST_PIPELINE_NAME> # e.g. <USER>-test-pipeline
export GDC_ORG=<ORG> # org name chosen in the destination GDCag environment
export GDC_CONSOLE_URL=<CONSOLE_URL> # console url of associated with the chosen org in the destination GDCag environment
```

developers could run `ci/configure.sh` to set up the test deployment pipeline in the test Concourse cluster,

```
./ci/configure.sh --cluster_name ${CONCOURSE_CLUSTER_NAME} --namespace ${GKE_CLUSTER_NS} --fly_target ${FLY_TARGET} --pipeline_name ${PIPELINE_NAME}
```

3. Log in the Concourse UI to check pipeline

After connecting to the Concourse cluster with `port-forward` setup following the cluster setup guide, developers could check the pipeline from the Concourse UI.

4. Manually trigger Concourse job

The Concourse job could be manually triggerd from the Concourse UI. Also, developers could manually trigger a job using `fly` command,

```
fly -t <FLY_TARGET> trigger-job -j <PIPELINE_NAME>/<JOB_NAME>
```

## Connect to the prod deployment pipeline

> [!NOTE]
> * Follow `Play and Test` if you are exploring rather than upgrading the prod deployment pipeline.
> * This Concourse instance is internal: access to the web UI and to `port-forward` is limited to Google. `fly` needs the `port-forward` below.

The deployment pipeline `cfbosh-deployment` running in the Concourse cluster `cfbosh-concourse-ci` is configured and triggered automatically by Stage 3 of the **`CFBOSH GDC release pipeline`** (`.github/workflows/release-pipeline.yaml`) via `ci/run-pipeline.sh --type prod` without developers' manual intervention. However, there are situations when maintainers need to monitor the deployment pipeline health and investigate deployment failures directly.

In case maintainers need to connect to the prod deployment pipeline, follow the steps below:

1. Authenticate to the GKE cluster and set up `port-forward` to connect to the Concourse web pod

```bash
gcloud container clusters get-credentials cfbosh --region us-east1 --project cfbosh-cicd
export POD_NAME=$(kubectl get pods --namespace cfbosh-concourse -l "app=cfbosh-concourse-ci-web" --field-selector=status.phase=Running -o jsonpath="{.items[0].metadata.name}")
kubectl port-forward --namespace cfbosh-concourse "$POD_NAME" 8080:8080
```

After the `port-forward` is set, the Concourse UI can be reached via `http://127.0.0.1:8080` from the local machine.

2. Retrieve the Concourse admin password and set up the `fly` target for the `cfbosh-deployment` pipeline

For the `prod` and `poc` Concourse clusters, the `admin` password is stored in `gs://cfbosh-cicd/concourse_local_users/local_user_credentials_for_prod_concourse.yaml` (parsed automatically by `get_admin_password` in `ci/utils.sh`):

```bash
CONCOURSE_ADMIN_PASSWORD=$(gcloud storage cat gs://cfbosh-cicd/concourse_local_users/local_user_credentials_for_prod_concourse.yaml \
  | yq -r '.[] | select(.path == "/secrets/localUsers?") | .value' \
  | tr ',' '\n' | awk -F: '$1 == "admin" {print $2; exit}')

fly -t cfbosh_prod_pipeline login -c http://127.0.0.1:8080 -u admin -p "${CONCOURSE_ADMIN_PASSWORD}"
```

Confirm the `fly` target is set, e.g.,

```
$ fly targets | grep cfbosh_prod_pipeline

cfbosh_prod_pipeline      http://127.0.0.1:8080  main  Sat, 06 Sep 2025 23:33:53 UTC
```

3. Check the jobs status

Developers could check the job status via the Concourse UI, or using the `fly` command, e.g.,

```
$ fly jobs -t cfbosh_prod_pipeline -p cfbosh-deployment

name                 paused  status     next
start-deployments    no      succeeded  n/a
bosh-bootstrap       no      succeeded  n/a
bosh-deployment      no      succeeded  n/a
run-bats             no      succeeded  n/a
cf-deployment        no      succeeded  n/a
run-cats             no      succeeded  n/a
pre-release-fan-in   no      succeeded  n/a
promote-candidate    no      succeeded  n/a
report-orphaned-vms  no      succeeded  n/a
```

When developers need to log in the Concourse worker container to deep dive the failure of certain job, reference `Check and debug failed job from local machine` section for details using the locally set `fly` target and pipeline named `cfbosh-deployment`.

# Check and debug failed job from local machine

There are needs to further investigate the failed Concourse jobs from developer's working machine. To do this, developers could execute the `fly` command below to step into the container executing the certain job,

```
fly -t <TARGET> intercept -u <JOB_ID_URL>
```

the `<TARGET>` is the `fly` target against the remote Concourse cluster set locally and the `<JOB_ID_URL>` is the url from the Concourse web when checking the failed job. Developers need to set the port-forward to be able to connect to the Concourse web, see the cluster setup guide.

For instance, if a locally set `fly` target is `cfbosh-pipeline` connecting to the deployment CI/CD pipeline running in the Concourse cluster, and the failed job url is `http://127.0.0.1:8080/teams/main/pipelines/cfbosh-deployment/jobs/bosh-deployment/builds/1`, developers to log in to the container for further debug the root cause of failed `bosh-deployment` job associated with certain build,

```
fly -t cfbosh-pipeline intercept -u http://127.0.0.1:8080/teams/main/pipelines/cfbosh-deployment/jobs/bosh-deployment/builds/1
```

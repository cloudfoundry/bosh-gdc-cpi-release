#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Deploy, trigger, and monitor the CFBOSH Concourse deployment pipeline on GKE.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

CONCOURSE_TYPE="${CONCOURSE_TYPE:-prod}"
FLY_TARGET="${FLY_TARGET:-cfbosh-concourse-gke}"
PIPELINE_NAME="${PIPELINE_NAME:-cfbosh-deployment}"
WAIT_JOB="${CONCOURSE_WAIT_JOB:-promote-candidate}"
CPI_VERSION="${CPI_VERSION:-}"
DRY_RUN="false"
ROTATE_KEY="false"
STATUS_FILE="${CONCOURSE_STATUS_FILE:-/tmp/concourse-status.json}"

STEMCELL_BUILDER_REPO_URL="${STEMCELL_BUILDER_REPO_URL:-https://github.com/cloudfoundry/bosh-linux-stemcell-builder.git}"
STEMCELL_BUILDER_BRANCH="${STEMCELL_BUILDER_BRANCH:-ubuntu-noble}"
STEMCELL_VERSION="${STEMCELL_VERSION:-}"
STEMCELL_BUILDER_TAG="${STEMCELL_BUILDER_TAG:-}"
STEMCELL_BUILDER_PR_REF="${STEMCELL_BUILDER_PR_REF:-}"

PORT_FORWARD_PID=""
TMP_DIR="$(mktemp -d -t ci-concourse-XXXXXX)"

cleanup() {
  if [[ -n "${PORT_FORWARD_PID}" ]] && kill -0 "${PORT_FORWARD_PID}" 2>/dev/null; then
    kill "${PORT_FORWARD_PID}" 2>/dev/null || true
    echo "[INFO] Terminated Concourse port-forward process ${PORT_FORWARD_PID}"
  fi
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

usage() {
  cat <<EOF
Usage: $0 [options]

Options:
  --type <prod|poc>        Target Concourse environment (default: prod)
  --fly-target <name>      Fly CLI target alias (default: cfbosh-concourse-gke)
  --wait-job <job>         Concourse job to watch after triggering:
                           none | start-deployments | bosh-bootstrap | bosh-deployment | promote-candidate
                           (default: promote-candidate)
  --cpi-version <version>  Candidate CPI release version in gs://cfbosh-cicd/cpi_releases/
  --stemcell-version <ver> Upstream GDC Ubuntu Noble stemcell version in gs://bosh-core-stemcells-candidate/gdch/
                           (default: auto-resolved latest version)
  --status-file <path>     Output JSON file for Concourse execution metadata
  --rotate-key             Rotate cfbosh-cicd-sa service account key before updating pipeline
  --dry-run                Validate and render pipeline manifest without applying or triggering
  -h, --help               Show this help message
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --type)
    CONCOURSE_TYPE="$2"
    shift 2
    ;;
  --fly-target | --fly_target)
    FLY_TARGET="$2"
    shift 2
    ;;
  --wait-job)
    WAIT_JOB="$2"
    shift 2
    ;;
  --cpi-version)
    CPI_VERSION="$2"
    shift 2
    ;;
  --stemcell-version)
    STEMCELL_VERSION="$2"
    STEMCELL_BUILDER_TAG="ubuntu-noble/v${STEMCELL_VERSION}"
    shift 2
    ;;
  --status-file)
    STATUS_FILE="$2"
    shift 2
    ;;
  --rotate-key)
    ROTATE_KEY="true"
    shift
    ;;
  --dry-run)
    DRY_RUN="true"
    shift
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    echo "[ERROR] Unknown argument: $1" >&2
    usage
    exit 1
    ;;
  esac
done

setup_gcp_auth() {
  if [[ -n "${CFBOSH_CICD_SERVICE_ACCOUNT_KEY:-}" ]]; then
    echo "[INFO] Authenticating GCP service account for Concourse CI/CD..."
    local sa_key_file="${TMP_DIR}/gcp-sa-key.json"
    umask 077
    printf '%s' "${CFBOSH_CICD_SERVICE_ACCOUNT_KEY}" >"${sa_key_file}"
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
      while IFS= read -r kline; do
        kline="${kline#"${kline%%[![:space:]]*}"}"
        kline="${kline%"${kline##*[![:space:]]}"}"
        if [[ -n "${kline}" && "${kline}" != "{" && "${kline}" != "}" ]]; then
          echo "::add-mask::${kline}"
        fi
      done < <(jq -r '(.private_key // empty), (.private_key_id // empty), (.client_id // empty), (.client_email // empty)' "${sa_key_file}" 2>/dev/null || true)
    fi
    gcloud auth activate-service-account --key-file="${sa_key_file}" --quiet >/dev/null 2>&1
    export GOOGLE_APPLICATION_CREDENTIALS="${sa_key_file}"
  fi
  gcloud config set project cfbosh-cicd --quiet >/dev/null 2>&1
}

ensure_dependencies() {
  echo "[INFO] Ensuring Concourse CLI dependencies (gke-gcloud-auth-plugin, bosh, fly, yq)..."
  export USE_GKE_GCLOUD_AUTH_PLUGIN=True
  mkdir -p "${HOME}/.local/bin"
  export PATH="${HOME}/.local/bin:${PATH}"

  if ! command -v gke-gcloud-auth-plugin >/dev/null 2>&1; then
    echo "[INFO] Installing gke-gcloud-auth-plugin..."
    local plugin_src
    plugin_src="$(curl -fsSL https://dl.google.com/dl/cloudsdk/channels/rapid/components-2.json | jq -r '.components[] | select(.id == "gke-gcloud-auth-plugin-linux-x86_64") | .data.source')"
    curl -fsSL "https://dl.google.com/dl/cloudsdk/channels/rapid/${plugin_src}" | tar -xzf - --strip-components=1 -C "${HOME}/.local/bin" bin/gke-gcloud-auth-plugin
    chmod +x "${HOME}/.local/bin/gke-gcloud-auth-plugin"
  fi

  if ! command -v bosh >/dev/null 2>&1; then
    echo "[INFO] Installing BOSH CLI v7.8.2..."
    curl -fsSL "https://github.com/cloudfoundry/bosh-cli/releases/download/v7.8.2/bosh-cli-7.8.2-linux-amd64" -o "${HOME}/.local/bin/bosh"
    chmod +x "${HOME}/.local/bin/bosh"
  fi

  if ! command -v fly >/dev/null 2>&1; then
    echo "[INFO] Installing Concourse fly CLI v7.13.2 from gs://cfbosh-cicd/fly/7.13.2/fly.tgz..."
    gcloud storage cp "gs://cfbosh-cicd/fly/7.13.2/fly.tgz" "${TMP_DIR}/fly.tgz"
    tar -xzf "${TMP_DIR}/fly.tgz" -C "${HOME}/.local/bin"
    chmod +x "${HOME}/.local/bin/fly"
  fi

  if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -qE 'mikefarah|v4\.'; then
    echo "[INFO] Installing yq v4.44.3..."
    curl -fsSL "https://github.com/mikefarah/yq/releases/download/v4.44.3/yq_linux_amd64" -o "${HOME}/.local/bin/yq"
    chmod +x "${HOME}/.local/bin/yq"
  fi
}

rotate_concourse_sa_key() {
  local key_file_name="cfbosh-cicd-sa-key.json"
  local service_account="cfbosh-cicd-sa@cfbosh-cicd.iam.gserviceaccount.com"
  local bucket_path="gs://cfbosh-cicd/cicd_sa_keys/cfbosh-cicd-sa"
  local local_key_path="${TMP_DIR}/${key_file_name}"

  echo "[INFO] Rotating service account key for Concourse CI/CD..."
  local existing_keys
  existing_keys="$(gcloud iam service-accounts keys list \
    --iam-account="${service_account}" \
    --managed-by=user \
    --format="value(name)")"

  gcloud iam service-accounts keys create "${local_key_path}" \
    --iam-account="${service_account}" \
    --quiet >/dev/null 2>&1
  if [[ "${GITHUB_ACTIONS:-}" == "true" && -s "${local_key_path}" ]]; then
    while IFS= read -r kline; do
      kline="${kline#"${kline%%[![:space:]]*}"}"
      kline="${kline%"${kline##*[![:space:]]}"}"
      if [[ -n "${kline}" && "${kline}" != "{" && "${kline}" != "}" ]]; then
        echo "::add-mask::${kline}"
      fi
    done < <(jq -r '(.private_key // empty), (.private_key_id // empty), (.client_id // empty), (.client_email // empty)' "${local_key_path}" 2>/dev/null || true)
  fi
  gcloud storage cp "${local_key_path}" "${bucket_path}/${key_file_name}" --quiet >/dev/null 2>&1
  rm -f "${local_key_path}"

  if [[ -n "${existing_keys}" ]]; then
    for key in ${existing_keys}; do
      echo "[INFO] Deleting old service account key..."
      gcloud iam service-accounts keys delete "${key}" \
        --iam-account="${service_account}" \
        --quiet >/dev/null 2>&1 || echo "[WARN] Could not delete old service account key"
    done
  fi
}

resolve_latest_stemcell_version() {
  if [[ -n "${STEMCELL_VERSION}" && "${STEMCELL_VERSION}" != "latest" ]]; then
    return 0
  fi
  echo "[INFO] Resolving latest GDC Ubuntu Noble stemcell version from gs://bosh-core-stemcells-candidate/gdch/..."
  local latest_ver=""
  latest_ver="$(curl -fsSL "https://storage.googleapis.com/storage/v1/b/bosh-core-stemcells-candidate/o?prefix=gdch/bosh-stemcell-" 2>/dev/null |
    jq -r '.items[]?.name // empty | capture("^gdch/bosh-stemcell-(?<v>[0-9]+\\.[0-9]+)-gdch-kvm-ubuntu-noble\\.tgz$") | .v' 2>/dev/null |
    sort -V | tail -n 1 || true)"
  if [[ -z "${latest_ver}" ]]; then
    echo "[INFO] Falling back to latest ${STEMCELL_BUILDER_BRANCH}/v* git tag from ${STEMCELL_BUILDER_REPO_URL}..."
    latest_ver="$(git ls-remote --tags --refs "${STEMCELL_BUILDER_REPO_URL}" "refs/tags/${STEMCELL_BUILDER_BRANCH}/v*" 2>/dev/null |
      awk -v prefix="refs/tags/${STEMCELL_BUILDER_BRANCH}/v" '{sub(prefix, "", $2); print $2}' |
      sort -V | tail -n 1 || true)"
  fi
  if [[ -z "${latest_ver}" ]]; then
    echo "[ERROR] Unable to resolve latest GDC Ubuntu Noble stemcell version." >&2
    exit 1
  fi
  STEMCELL_VERSION="${latest_ver}"
  echo "[INFO] Resolved latest GDC Ubuntu Noble stemcell version: ${STEMCELL_VERSION}"
}

prepare_stemcell_builder_repo() {
  resolve_latest_stemcell_version
  if [[ -z "${STEMCELL_BUILDER_TAG:-}" ]]; then
    STEMCELL_BUILDER_TAG="${STEMCELL_BUILDER_BRANCH}/v${STEMCELL_VERSION}"
  fi
  local stemcell_dir="${TMP_DIR}/bosh-linux-stemcell-builder"
  rm -rf "${stemcell_dir}"
  echo "[INFO] Cloning stemcell builder from ${STEMCELL_BUILDER_REPO_URL} (branch: ${STEMCELL_BUILDER_BRANCH})..."
  git clone --depth 1 --branch "${STEMCELL_BUILDER_BRANCH}" --single-branch "${STEMCELL_BUILDER_REPO_URL}" "${stemcell_dir}"
  git config --global --add safe.directory "${stemcell_dir}"
  if [[ -n "${STEMCELL_BUILDER_PR_REF:-}" ]]; then
    echo "[INFO] Checking out PR ref ${STEMCELL_BUILDER_PR_REF}..."
    git -C "${stemcell_dir}" fetch --depth 1 origin "${STEMCELL_BUILDER_PR_REF}:stemcell-pr"
    git -C "${stemcell_dir}" -c advice.detachedHead=false checkout stemcell-pr
  elif [[ -n "${STEMCELL_BUILDER_TAG:-}" ]]; then
    echo "[INFO] Checking out stemcell release tag ${STEMCELL_BUILDER_TAG}..."
    if git -C "${stemcell_dir}" fetch --depth 1 origin "refs/tags/${STEMCELL_BUILDER_TAG}:refs/tags/${STEMCELL_BUILDER_TAG}" 2>/dev/null; then
      git -C "${stemcell_dir}" -c advice.detachedHead=false checkout "tags/${STEMCELL_BUILDER_TAG}"
    else
      echo "[WARN] Tag ${STEMCELL_BUILDER_TAG} not found in ${STEMCELL_BUILDER_REPO_URL}; using ${STEMCELL_BUILDER_BRANCH} branch HEAD."
    fi
  fi
  local stemcell_commit
  stemcell_commit="$(git -C "${stemcell_dir}" rev-parse --short HEAD)"
  echo "[INFO] Stemcell builder ready at commit ${stemcell_commit} (${STEMCELL_BUILDER_REPO_URL} ${STEMCELL_BUILDER_PR_REF:-${STEMCELL_BUILDER_TAG:-${STEMCELL_BUILDER_BRANCH}}})"
}

trigger_and_monitor_pipeline() {
  local namespace="cfbosh-concourse"
  local cluster_name="cfbosh-concourse-ci"
  if [[ "${CONCOURSE_TYPE}" == "poc" ]]; then
    namespace="cfbosh-concourse-poc"
    cluster_name="cfbosh-concourse-poc"
  fi

  export WORK_DIR="${TMP_DIR}/concourse-work"
  mkdir -p "${WORK_DIR}"
  export NAMESPACE="${namespace}"
  export CLUSTER_NAME="${cluster_name}"

  # Source helper functions from ci/utils.sh
  # shellcheck source=ci/utils.sh
  source "${SCRIPT_DIR}/utils.sh"

  local admin_pass
  admin_pass="$(get_admin_password)"
  if [[ "${GITHUB_ACTIONS:-}" == "true" && -n "${admin_pass}" ]]; then
    echo "::add-mask::${admin_pass}"
  fi

  ensure_concourse_connection() {
    if [[ -z "${CONCOURSE_CLUSTER_URL:-}" ]] || ! curl -ks --max-time 5 "${CONCOURSE_CLUSTER_URL}/api/v1/info" >/dev/null 2>&1; then
      if [[ -n "${PORT_FORWARD_PID:-}" ]] && kill -0 "${PORT_FORWARD_PID}" 2>/dev/null; then
        kill "${PORT_FORWARD_PID}" 2>/dev/null || true
      fi
      connect_to_concourse_cluster
      fly -t "${FLY_TARGET}" login -c "${CONCOURSE_CLUSTER_URL}" -u admin -p "${admin_pass}" >/dev/null
    fi
  }

  ensure_concourse_connection

  echo "[INFO] Ensuring Concourse pipeline '${PIPELINE_NAME}' is unpaused..."
  fly -t "${FLY_TARGET}" unpause-pipeline -p "${PIPELINE_NAME}"

  if [[ -n "${CPI_VERSION}" ]]; then
    echo "[INFO] Verifying candidate CPI artifact in gs://cfbosh-cicd/cpi_releases/bosh_gdch_cpi_release_${CPI_VERSION}.tgz..."
    gcloud storage ls "gs://cfbosh-cicd/cpi_releases/bosh_gdch_cpi_release_${CPI_VERSION}.tgz"
  fi

  if [[ -n "${STEMCELL_VERSION}" ]]; then
    local stemcell_url="https://storage.googleapis.com/bosh-core-stemcells-candidate/gdch/bosh-stemcell-${STEMCELL_VERSION}-gdch-kvm-ubuntu-noble.tgz"
    echo "[INFO] Verifying upstream GDC stemcell artifact in gs://bosh-core-stemcells-candidate/gdch/bosh-stemcell-${STEMCELL_VERSION}-gdch-kvm-ubuntu-noble.tgz..."
    curl -fsSI "${stemcell_url}" >/dev/null
  fi

  echo "[INFO] Checking Concourse resource '${PIPELINE_NAME}/bosh-cpi-releases'..."
  fly -t "${FLY_TARGET}" check-resource -r "${PIPELINE_NAME}/bosh-cpi-releases"

  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    case "${WAIT_JOB}" in
    none | start-deployments | bosh-bootstrap)
      echo "[WARN] WAIT_JOB='${WAIT_JOB}' exits before 'bosh-deployment' completes and can mask deployment failures; upgrading WAIT_JOB to 'bosh-deployment'."
      WAIT_JOB="bosh-deployment"
      ;;
    esac
  fi

  local pre_jobs_json="[]"
  pre_jobs_json="$(fly -t "${FLY_TARGET}" jobs -p "${PIPELINE_NAME}" --json 2>/dev/null || echo '[]')"
  local active_job=""
  active_job="$(echo "${pre_jobs_json}" | jq -r '.[] | select(.next_build.status == "started") | "\(.name) #\(.next_build.name)"' | head -n 1 || true)"

  local baseline_target_id=0
  baseline_target_id="$(echo "${pre_jobs_json}" | jq -r --arg j "${WAIT_JOB}" '(.[] | select(.name == $j) | .finished_build.id) // 0')"
  local baseline_max_build_id=0
  baseline_max_build_id="$(echo "${pre_jobs_json}" | jq -r '[.[] | .finished_build.id // 0] | max // 0')"

  local build_num=""
  if [[ -n "${active_job}" ]]; then
    build_num="$(echo "${pre_jobs_json}" | jq -r '.[] | select(.name == "start-deployments") | .finished_build.name // "latest"')"
    echo "[INFO] Active Concourse deployment is currently running (${active_job}, from start-deployments #${build_num}); monitoring through '${WAIT_JOB}'..."
  else
    echo "[INFO] Triggering Concourse job '${PIPELINE_NAME}/start-deployments'..."
    local trigger_out
    trigger_out="$(fly -t "${FLY_TARGET}" trigger-job -j "${PIPELINE_NAME}/start-deployments" 2>&1 | tee /dev/stderr)"
    build_num="$(echo "${trigger_out}" | sed -n 's/.*started .* #\([0-9][0-9]*\)$/\1/p' | tail -n 1)"
    echo "[INFO] Triggered ${PIPELINE_NAME}/start-deployments build #${build_num:-latest}"
  fi

  if [[ "${WAIT_JOB}" != "none" ]]; then
    echo "[INFO] Monitoring Concourse pipeline '${PIPELINE_NAME}' until '${WAIT_JOB}' completes (baseline build id=${baseline_target_id})..."
    local watched_builds=" "
    local reported_finished=" "
    local retried_jobs=" "
    local idle_polls=0
    while true; do
      ensure_concourse_connection
      local cur_jobs_json
      cur_jobs_json="$(fly -t "${FLY_TARGET}" jobs -p "${PIPELINE_NAME}" --json 2>/dev/null || echo '[]')"

      # Report completion status for any previously started job builds without streaming raw worker stdout/stderr
      local finished_entries
      finished_entries="$(echo "${cur_jobs_json}" | jq -r '.[] | select(.finished_build != null) | "\(.name):\(.finished_build.name):\(.finished_build.status)"')"
      if [[ -n "${finished_entries}" ]]; then
        while IFS=: read -r f_name f_build f_status; do
          [[ -z "${f_name}" || -z "${f_build}" ]] && continue
          if [[ "${watched_builds}" == *" ${f_name}:${f_build} "* && "${reported_finished}" != *" ${f_name}:${f_build} "* ]]; then
            reported_finished="${reported_finished}${f_name}:${f_build} "
            echo "[INFO] Concourse job '${PIPELINE_NAME}/${f_name}' build #${f_build} finished with status '${f_status}'."
          fi
        done <<<"${finished_entries}"
      fi

      # Check if target WAIT_JOB finished a new build since baseline_target_id
      local target_fin_id target_fin_status target_fin_name
      target_fin_id="$(echo "${cur_jobs_json}" | jq -r --arg j "${WAIT_JOB}" '(.[] | select(.name == $j) | .finished_build.id) // 0')"
      target_fin_status="$(echo "${cur_jobs_json}" | jq -r --arg j "${WAIT_JOB}" '(.[] | select(.name == $j) | .finished_build.status) // ""')"
      target_fin_name="$(echo "${cur_jobs_json}" | jq -r --arg j "${WAIT_JOB}" '(.[] | select(.name == $j) | .finished_build.name) // ""')"

      if ((target_fin_id > baseline_target_id)); then
        if [[ "${target_fin_status}" == "succeeded" ]]; then
          echo "[INFO] Target Concourse job '${PIPELINE_NAME}/${WAIT_JOB}' build #${target_fin_name} succeeded."
          break
        else
          echo "[ERROR] Target Concourse job '${PIPELINE_NAME}/${WAIT_JOB}' build #${target_fin_name} finished with status '${target_fin_status}'." >&2
          exit 1
        fi
      fi

      # Log any job currently in 'started' state without running 'fly watch' (prevents internal
      # Concourse task stdout/stderr from being streamed to public GitHub Actions logs)
      local started_entries
      started_entries="$(echo "${cur_jobs_json}" | jq -r '.[] | select(.next_build.status == "started") | "\(.name):\(.next_build.name)"')"
      if [[ -n "${started_entries}" ]]; then
        idle_polls=0
        while IFS=: read -r j_name j_build; do
          [[ -z "${j_name}" || -z "${j_build}" ]] && continue
          if [[ "${watched_builds}" != *" ${j_name}:${j_build} "* ]]; then
            watched_builds="${watched_builds}${j_name}:${j_build} "
            echo "[INFO] Concourse job '${PIPELINE_NAME}/${j_name}' build #${j_build} is running (logs retained in internal Concourse cluster)..."
          fi
        done <<<"${started_entries}"
        sleep 15
        continue
      fi

      # If no deployment jobs have a started or pending build anymore, wait a few polls
      # for Concourse's scheduler to transition between upstream 'put' and downstream trigger
      local active_count
      active_count="$(echo "${cur_jobs_json}" | jq '[.[] | select(.name != "report-orphaned-vms" and .next_build != null)] | length')"
      if ((active_count == 0)); then
        idle_polls=$((idle_polls + 1))
        if ((idle_polls >= 3)); then
          local failed_job_name failed_job_desc
          failed_job_name="$(echo "${cur_jobs_json}" | jq -r --argjson base "${baseline_max_build_id}" '.[] | select(.name != "report-orphaned-vms" and (.paused | not) and ((.finished_build.id // 0) > $base) and (.finished_build.status == "failed" or .finished_build.status == "errored")) | .name' | head -n 1 || true)"
          failed_job_desc="$(echo "${cur_jobs_json}" | jq -r --argjson base "${baseline_max_build_id}" '.[] | select(.name != "report-orphaned-vms" and (.paused | not) and ((.finished_build.id // 0) > $base) and (.finished_build.status == "failed" or .finished_build.status == "errored" or .finished_build.status == "aborted")) | "\(.name) #\(.finished_build.name) (\(.finished_build.status))"' | head -n 1 || true)"
          if [[ -n "${failed_job_name}" && "${retried_jobs}" != *" ${failed_job_name} "* ]]; then
            retried_jobs="${retried_jobs}${failed_job_name} "
            idle_polls=0
            echo "[WARN] Concourse job '${failed_job_desc}' failed; re-triggering '${PIPELINE_NAME}/${failed_job_name}' once..."
            ensure_concourse_connection
            fly -t "${FLY_TARGET}" trigger-job -j "${PIPELINE_NAME}/${failed_job_name}" || true
            sleep 15
            continue
          fi
          if [[ -n "${failed_job_desc}" ]]; then
            echo "[ERROR] Concourse pipeline halted before '${WAIT_JOB}' completed; failed job: ${failed_job_desc}" >&2
            exit 1
          fi
        fi
      else
        idle_polls=0
      fi

      sleep 15
    done
  fi

  local latest_log=""
  latest_log="$(gcloud storage ls "gs://cfbosh-cicd/cicd-deployment-logs/log_*.md" 2>/dev/null | sort | tail -n 1 || true)"
  if [[ -n "${latest_log}" ]]; then
    echo "[INFO] Latest Concourse deployment log recorded in GCS: ${latest_log}"
  fi

  echo "[INFO] Current Concourse jobs status in '${PIPELINE_NAME}':"
  fly -t "${FLY_TARGET}" jobs -p "${PIPELINE_NAME}"

  local jobs_json="[]"
  jobs_json="$(fly -t "${FLY_TARGET}" jobs -p "${PIPELINE_NAME}" --json 2>/dev/null || echo '[]')"

  jq -n \
    --arg status "success" \
    --arg pipeline "${PIPELINE_NAME}" \
    --arg cluster "${cluster_name}" \
    --arg start_build "${build_num:-latest}" \
    --arg wait_job "${WAIT_JOB}" \
    --arg latest_log "${latest_log}" \
    --arg cpi_version "${CPI_VERSION}" \
    --arg stemcell_version "${STEMCELL_VERSION}" \
    --argjson jobs "${jobs_json}" \
    '{
      status: $status,
      pipeline: $pipeline,
      cluster: $cluster,
      start_deployments_build: $start_build,
      wait_job: $wait_job,
      latest_deployment_log: $latest_log,
      cpi_version: $cpi_version,
      stemcell_version: $stemcell_version,
      jobs: ($jobs | map({name: .name, paused: .paused, status: (.finished_build.status // "pending"), next_status: (.next_build.status // "idle")}))
    }' >"${STATUS_FILE}"

  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "concourse_status=success"
      echo "concourse_build=${build_num:-latest}"
      echo "concourse_log_uri=${latest_log}"
    } >>"${GITHUB_OUTPUT}"
  fi
}

main() {
  cd "${REPO_ROOT}"
  setup_gcp_auth
  ensure_dependencies

  if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
    [[ -n "${GDC_ORG:-}" ]] && echo "::add-mask::${GDC_ORG}"
    [[ -n "${GDC_CONSOLE_URL:-}" ]] && echo "::add-mask::${GDC_CONSOLE_URL}"
  fi

  if [[ "${ROTATE_KEY}" == "true" ]]; then
    rotate_concourse_sa_key
  fi

  prepare_stemcell_builder_repo

  local configure_args=(
    "--skip-login"
    "--type" "${CONCOURSE_TYPE}"
    "--fly_target" "${FLY_TARGET}"
  )
  if [[ "${DRY_RUN}" == "true" ]]; then
    configure_args+=("--dry-run")
  fi

  echo "[INFO] Configuring Concourse deployment pipeline (${configure_args[*]})..."
  "${SCRIPT_DIR}/configure.sh" "${configure_args[@]}"

  if [[ "${DRY_RUN}" == "true" ]]; then
    echo "[INFO] Dry-run completed successfully."
    exit 0
  fi

  trigger_and_monitor_pipeline
}

main "$@"

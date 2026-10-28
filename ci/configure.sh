#!/bin/bash
# Copyright 2025 Google LLC
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

# Fail on any error.
set -eu -o pipefail

# Paths are relative to this script, so it runs from any checkout without Bazel.
CI_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=utils.sh
source "${CI_DIR}/utils.sh"

GCP_PROJECT="cfbosh-cicd"
CICD_SA_KEY_PATH="gs://cfbosh-cicd/cicd_sa_keys/cfbosh-cicd-sa/cfbosh-cicd-sa-key.json"

WORK_DIR="${CI_DIR}/pipeline"
BASE_MANIFEST="${WORK_DIR}/base.yml"
CONFIGS_TEMPLATE="${WORK_DIR}/configs_template.yml"

CLUSTER_TYPE=""
CONCOURSE_ADMIN_PASSWORD="admin"
DRY_RUN=false
SKIP_LOGIN=false

validate_inputs() {
  local errors=()

  if [[ -z "${GDC_ORG:-}" ]]; then
    errors+=("* GDC_ORG env var is required (missing).")
  fi
  if [[ -z "${GDC_CONSOLE_URL:-}" ]]; then
    errors+=("* GDC_CONSOLE_URL env var is required (missing).")
  fi

  if [[ ${#errors[@]} -gt 0 ]]; then
    local IFS=$'\n'
    echo -e "[ERROR] Invalid configuration(s):\n${errors[*]}" >&2
    exit 1
  fi
}

while [[ "$#" -gt 0 ]]; do
  case "$1" in
  --type)
    CLUSTER_TYPE=$2
    shift 2

    case "${CLUSTER_TYPE}" in
    prod)
      echo "[INFO] Setting up pipeline in prod Concourse cluster"
      CLUSTER_NAME=cfbosh-concourse-ci
      NAMESPACE=cfbosh-concourse
      PIPELINE_NAME=cfbosh-deployment
      CONCOURSE_ADMIN_PASSWORD=$(get_admin_password)
      if [[ "${GITHUB_ACTIONS:-}" == "true" && -n "${CONCOURSE_ADMIN_PASSWORD}" ]]; then
        echo "::add-mask::${CONCOURSE_ADMIN_PASSWORD}"
      fi
      ;;
    poc)
      echo "[INFO] Setting up pipeline in poc Concourse cluster"
      CLUSTER_NAME=cfbosh-concourse-ci-poc
      NAMESPACE=cfbosh-concourse-poc
      PIPELINE_NAME=cfbosh-deployment-playground
      CONCOURSE_ADMIN_PASSWORD=$(get_admin_password)
      if [[ "${GITHUB_ACTIONS:-}" == "true" && -n "${CONCOURSE_ADMIN_PASSWORD}" ]]; then
        echo "::add-mask::${CONCOURSE_ADMIN_PASSWORD}"
      fi
      ;;
    *)
      echo "[INFO] Setting up pipeline in ${CLUSTER_TYPE} Concourse cluster for test purpose"
      ;;
    esac

    ;;
  --dry-run)
    DRY_RUN=true
    echo "[INFO] Dry run mode on"
    shift
    ;;
  --skip-login)
    SKIP_LOGIN=true
    echo "[INFO] Skip gcloud auth login"
    shift
    ;;
  --cluster_name)
    CLUSTER_NAME=$2
    shift 2
    ;;
  --namespace)
    NAMESPACE=$2
    shift 2
    ;;
  --fly_target)
    FLY_TARGET=$2
    shift 2
    ;;
  --pipeline_name)
    PIPELINE_NAME=$2
    shift 2
    ;;
  *)
    echo "[ERROR] Unknown parameter passed: $1" >&2
    exit 1
    ;;
  esac
done

# Clean up the temporary directory on EXIT
trap 'cleanup' EXIT

main() {
  validate_inputs

  cd "${WORK_DIR}"

  # gcloud auth login on behalf of human user
  if [[ "${SKIP_LOGIN}" != "true" ]]; then
    gcloud auth login
  fi
  gcloud container clusters get-credentials cfbosh --region us-east1 --project "${GCP_PROJECT}" --quiet >/dev/null 2>&1

  # Download `cfbosh-cicd-sa` key silently
  gcloud storage cp "${CICD_SA_KEY_PATH}" cfbosh-cicd-sa-key.json --quiet >/dev/null 2>&1
  chmod 600 cfbosh-cicd-sa-key.json
  if [[ "${GITHUB_ACTIONS:-}" == "true" && -s cfbosh-cicd-sa-key.json ]]; then
    while IFS= read -r kline; do
      kline="${kline#"${kline%%[![:space:]]*}"}"
      kline="${kline%"${kline##*[![:space:]]}"}"
      if [[ -n "${kline}" && "${kline}" != "{" && "${kline}" != "}" ]]; then
        echo "::add-mask::${kline}"
      fi
    done < <(jq -r '(.private_key // empty), (.private_key_id // empty), (.client_id // empty), (.client_email // empty)' cfbosh-cicd-sa-key.json 2>/dev/null || true)
  fi

  # Use BOSH CLI as the yaml rendering engine to get the manifests (manifest.yml and configs.yml) for installing pipeline on Concourse cluster
  bosh int "${BASE_MANIFEST}" \
    -o jobs_skeleton.yml \
    -o start_deploy_job.yml \
    -o bosh_bootstrap_job.yml \
    -o bosh_deploy_job.yml \
    -o bats_job.yml \
    -o cf_deploy_job.yml \
    -o cats_job.yml \
    -o pre_release_fan_in_job.yml \
    -o prod_release_job.yml \
    -o orphaned_vms_job.yml >manifest.yml

  bosh int "${CONFIGS_TEMPLATE}" \
    --var org="${GDC_ORG}" \
    --var console_url="${GDC_CONSOLE_URL}" \
    --var-file cicd_sa_key_json=cfbosh-cicd-sa-key.json >configs.yml

  connect_to_concourse_cluster
  setup_pipeline

  cd -
}

cleanup() {
  cd "${WORK_DIR}"
  # Clean up manifest.yml configs.yml cfbosh-cicd-sa-key.json generated during the script execution
  rm -rf manifest.yml configs.yml cfbosh-cicd-sa-key.json local_user_credentials.yaml
  cd -

  # Kill the port-forward process
  if [[ -n "${PORT_FORWARD_PID:-}" ]] && kill -0 "${PORT_FORWARD_PID}" 2>/dev/null; then
    kill "${PORT_FORWARD_PID}" 2>/dev/null || true
    wait "${PORT_FORWARD_PID}" 2>/dev/null || true
    echo "[INFO] Killed the port-forward process ${PORT_FORWARD_PID}"
  fi
}

main "$@"
exit

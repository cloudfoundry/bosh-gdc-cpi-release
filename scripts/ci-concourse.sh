#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Google LLC
#
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(pwd)"

# shellcheck source=scripts/ci-common.sh
source "${SCRIPT_DIR}/ci-common.sh"

export CI_WORKFLOW_TYPE="concourse"
export CI_COMMENT_TRIGGER="/test-concourse"
check_ci_preconditions

CONCOURSE_MODE="${CONCOURSE_MODE:-dry-run}"
CONCOURSE_TMP_DIR="/tmp/ci-concourse-$$"
CONCOURSE_BIN_DIR="${CONCOURSE_TMP_DIR}/bin"
GCP_SA_KEY_FILE="${CONCOURSE_TMP_DIR}/cfbosh_cicd_sa.json"

export FLY_TARGET="${FLY_TARGET:-cfbosh-concourse-gke}"

mkdir -p "${CONCOURSE_BIN_DIR}"
export PATH="${CONCOURSE_BIN_DIR}:${PATH}"

cleanup() {
  local exit_code=$?
  rm -rf "${CONCOURSE_TMP_DIR}"
  complete_pr_check_run "${exit_code}"
}
trap cleanup EXIT

ensure_concourse_tools() {
  local gcloud_sdk_root
  gcloud_sdk_root=$(gcloud info --format="value(installation.sdk_root)" 2>/dev/null || true)
  if [[ -n "${gcloud_sdk_root}" && -d "${gcloud_sdk_root}/bin" ]]; then
    export PATH="${gcloud_sdk_root}/bin:${PATH}"
  fi

  if ! command -v gke-gcloud-auth-plugin >/dev/null 2>&1; then
    echo "[INFO] Installing gke-gcloud-auth-plugin..."
    local plugin_src
    plugin_src=$(curl -fsSL https://dl.google.com/dl/cloudsdk/channels/rapid/components-2.json |
      jq -r '.components[] | select(.id == "gke-gcloud-auth-plugin-linux-x86_64") | .data.source' | head -n 1)
    curl -fsSL "https://dl.google.com/dl/cloudsdk/channels/rapid/${plugin_src}" |
      tar -xzf - -C "${CONCOURSE_TMP_DIR}"
    chmod +x "${CONCOURSE_BIN_DIR}/gke-gcloud-auth-plugin"
  fi
  export USE_GKE_GCLOUD_AUTH_PLUGIN=True

  if ! command -v yq >/dev/null 2>&1; then
    local yq_version="v4.43.1"
    echo "[INFO] Installing yq ${yq_version}..."
    curl -fsSL "https://github.com/mikefarah/yq/releases/download/${yq_version}/yq_linux_amd64" -o "${CONCOURSE_BIN_DIR}/yq"
    chmod +x "${CONCOURSE_BIN_DIR}/yq"
  fi

  if ! command -v bosh >/dev/null 2>&1; then
    local bosh_version="7.8.2"
    echo "[INFO] Installing BOSH CLI v${bosh_version}..."
    curl -fsSL "https://github.com/cloudfoundry/bosh-cli/releases/download/v${bosh_version}/bosh-cli-${bosh_version}-linux-amd64" -o "${CONCOURSE_BIN_DIR}/bosh"
    chmod +x "${CONCOURSE_BIN_DIR}/bosh"
  fi

  if ! command -v fly >/dev/null 2>&1; then
    local fly_version="7.13.2"
    echo "[INFO] Installing Concourse fly CLI v${fly_version}..."
    gcloud storage cp "gs://cfbosh-cicd/fly/${fly_version}/fly.tgz" "${CONCOURSE_TMP_DIR}/fly.tgz"
    tar -xzf "${CONCOURSE_TMP_DIR}/fly.tgz" -C "${CONCOURSE_BIN_DIR}"
    chmod +x "${CONCOURSE_BIN_DIR}/fly"
    rm -f "${CONCOURSE_TMP_DIR}/fly.tgz"
  fi
}

rotate_cicd_sa_key() {
  local sa_name="cfbosh-cicd-sa"
  local gcp_project="cfbosh-cicd"
  local sa_email="${sa_name}@${gcp_project}.iam.gserviceaccount.com"
  local sa_key_gcs_path="gs://cfbosh-cicd/cicd_sa_keys/cfbosh-cicd-sa/cfbosh-cicd-sa-key.json"
  local new_key_file="${CONCOURSE_TMP_DIR}/new-cfbosh-cicd-sa-key.json"

  local active_key_id=""
  if [[ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" && -f "${GOOGLE_APPLICATION_CREDENTIALS}" ]]; then
    active_key_id=$(jq -r '.private_key_id // empty' "${GOOGLE_APPLICATION_CREDENTIALS}" 2>/dev/null || true)
  fi

  local existing_keys
  existing_keys=$(gcloud iam service-accounts keys list \
    --iam-account="${sa_email}" \
    --managed-by=user \
    --format="value(name)")

  gcloud iam service-accounts keys create "${new_key_file}" \
    --iam-account="${sa_email}"

  for key in ${existing_keys}; do
    local key_id="${key##*/}"
    if [[ -n "${active_key_id}" && "${key_id}" == "${active_key_id}" ]]; then
      echo "[INFO] Preserving active CI runner key ${key_id}"
      continue
    fi
    gcloud iam service-accounts keys delete "${key}" \
      --iam-account="${sa_email}" \
      --quiet
  done

  gcloud storage cp "${new_key_file}" "${sa_key_gcs_path}"
  rm -f "${new_key_file}"
}

verify_clean_working_tree() {
  echo "Checking for uncommitted changes and untracked files..."
  cd "${REPO_ROOT}"

  local diff_changes_found=false
  local untracked_files_found=false
  local error_message=""

  if ! git diff HEAD --quiet; then
    diff_changes_found=true
    error_message+=" - Uncommitted changes detected (staged or unstaged).\n"
  fi

  if [[ -n "$(git ls-files --others --exclude-standard)" ]]; then
    untracked_files_found=true
    error_message+=" - Untracked files detected.\n"
  fi

  if [[ "${diff_changes_found}" == "true" || "${untracked_files_found}" == "true" ]]; then
    printf "Error: Working directory is not clean.\n%b" "${error_message}" >&2
    if [[ "${diff_changes_found}" == "true" ]]; then
      echo "--- Diff Output ---" >&2
      git diff HEAD >&2
    fi
    if [[ "${untracked_files_found}" == "true" ]]; then
      echo "--- Untracked Files ---" >&2
      git ls-files --others --exclude-standard >&2
    fi
    echo "Please make sure to run ./ci/configure.sh on your local workspace to apply these changes." >&2
    exit 1
  fi

  echo "Success: No uncommitted changes or untracked files found."
}

echo "=================================================================="
echo "Setting up GCP CI/CD credentials for Concourse..."
echo "=================================================================="
setup_gcp_sa_key "${GCP_SA_KEY_FILE}" "${CFBOSH_CICD_SERVICE_ACCOUNT_KEY:-}"
export CLOUDSDK_CONFIG="${CONCOURSE_TMP_DIR}/gcloud"
export KUBECONFIG="${CONCOURSE_TMP_DIR}/kubeconfig"
export GOOGLE_APPLICATION_CREDENTIALS="${GCP_SA_KEY_FILE}"
gcloud auth activate-service-account --key-file="${GCP_SA_KEY_FILE}"

echo "=================================================================="
echo "Ensuring Concourse CLI dependencies are installed..."
echo "=================================================================="
ensure_concourse_tools

cd "${REPO_ROOT}"

case "${CONCOURSE_MODE}" in
rotate-key)
  echo "=================================================================="
  echo "Rotating cfbosh-cicd-sa service account key and updating pipeline..."
  echo "=================================================================="
  rotate_cicd_sa_key
  ./ci/configure.sh --skip-login \
    --type prod \
    --fly_target "${FLY_TARGET}"
  ;;
update-pipeline | apply)
  echo "=================================================================="
  echo "Applying Concourse deployment pipeline configuration..."
  echo "=================================================================="
  ./ci/configure.sh --skip-login \
    --type prod \
    --fly_target "${FLY_TARGET}"
  ;;
dry-run)
  echo "=================================================================="
  echo "Validating Concourse deployment pipeline configuration (--dry-run)..."
  echo "=================================================================="
  ./ci/configure.sh --skip-login \
    --type prod \
    --fly_target "${FLY_TARGET}" \
    --dry-run
  ;;
*)
  echo "::error::Unsupported CONCOURSE_MODE '${CONCOURSE_MODE}'. Must be 'dry-run', 'apply', 'update-pipeline', or 'rotate-key'."
  exit 1
  ;;
esac

verify_clean_working_tree

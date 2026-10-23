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

# Helper functions for installing or updating the Concourse pipelines running in the Concourse cluster on GKE

# Fail on any error.
set -eu -o pipefail

# Note: this method needs to be defined on the top so the shell argument parser can reference
# Get the admin password of the prod Concourse deployment
get_admin_password() {
  cd "${WORK_DIR}"

  local remote_cred_file="gs://cfbosh-cicd/concourse_local_users/local_user_credentials_for_prod_concourse.yaml"
  local download_cred_file="local_user_credentials.yaml"

  echo "[INFO] Download Concourse local user credentials" >&2
  gcloud storage cp "${remote_cred_file}" "${download_cred_file}"

  if [ ! -f "$download_cred_file" ]; then
    echo "[ERROR] Credentials file not successfully downloaded at $download_cred_file" >&2
    exit 1
  fi

  local value=$(yq -r '.[] | select(.path == "/secrets/localUsers?") | .value' "$download_cred_file")

  if [ -z "$value" ]; then
    echo "[ERROR] Could not find secrets/localUsers? path or value is empty" >&2
    exit 1
  fi

  # Parse the admin user password
  IFS=',' read -ra tokens <<<"$value"
  for token in "${tokens[@]}"; do
    if [[ "$token" == admin:* ]]; then
      echo "${token#admin:}"
      exit 0
    fi
  done

  echo "[ERROR] Admin password not found in value: $value" >&2
  exit 1
}

# Set up the port-forward in the background to establish the connection to the Concourse cluster
connect_to_concourse_cluster() {
  export POD_NAME=$(kubectl get pods --namespace ${NAMESPACE} -l "app=${CLUSTER_NAME}-web" --field-selector=status.phase=Running -o jsonpath="{.items[0].metadata.name}")

  # Exit if the pod isn't found
  if [[ -z "${POD_NAME}" ]]; then
    echo "[ERROR] Could not find Concourse web pod in namespace ${NAMESPACE}"
    exit 1
  fi

  kubectl port-forward --namespace ${NAMESPACE} $POD_NAME 8080:8080 &>/dev/null &
  # Get the process ID of the port-forward
  PORT_FORWARD_PID=$!

  CONCOURSE_CLUSTER_URL="http://127.0.0.1:8080"
  port_check_path="/dev/tcp/127.0.0.1/8080"

  # Wait up to 30 seconds for the port to become available
  timeout=30
  end_time=$((SECONDS + timeout))

  while [[ $SECONDS -lt $end_time ]]; do
    # Check if port is active
    if (echo >"${port_check_path}") &>/dev/null; then
      echo "[INFO] Concourse cluster ${CLUSTER_NAME} is connected with local URL ${CONCOURSE_CLUSTER_URL} via port-forward"
      return
    fi
    sleep 1
  done
  echo "[ERROR] Port-forward failed to start in time."
  exit 1
}

# Set the Concourse pipeline (installing or updating) running in the targeted Concourse cluster
setup_pipeline() {
  # Login the Concourse cluster
  fly -t ${FLY_TARGET} login -c "${CONCOURSE_CLUSTER_URL}" -u admin -p "${CONCOURSE_ADMIN_PASSWORD}"
  fly -t ${FLY_TARGET} sync

  # Set up or update deployment pipline in the Concourse cluster
  if [[ "${DRY_RUN}" == "true" ]]; then
    fly -t ${FLY_TARGET} set-pipeline -p ${PIPELINE_NAME} -c manifest.yml -l configs.yml --dry-run
    exit
  fi

  fly -t ${FLY_TARGET} set-pipeline -p ${PIPELINE_NAME} -c manifest.yml -l configs.yml --non-interactive
  echo "[INFO] Deployment pipeline ${PIPELINE_NAME} in Concourse cluster ${CLUSTER_NAME} was set up successfully!"
}

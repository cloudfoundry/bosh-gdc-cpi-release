#!/bin/bash
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

# Shared helper functions for discovering and downloading the upstream GDC
# Ubuntu Noble stemcell from gs://bosh-core-stemcells-candidate/gdch/.

STEMCELL_CANDIDATE_BUCKET="bosh-core-stemcells-candidate"
STEMCELL_CANDIDATE_API_URL="https://storage.googleapis.com/storage/v1/b/${STEMCELL_CANDIDATE_BUCKET}/o?prefix=gdch/bosh-stemcell-"
STEMCELL_CANDIDATE_BASE_URL="https://storage.googleapis.com/${STEMCELL_CANDIDATE_BUCKET}"

resolve_latest_stemcell_object() {
  curl -fsSL "${STEMCELL_CANDIDATE_API_URL}" 2>/dev/null |
    jq -r '.items[]?.name // empty | select(test("^gdch/bosh-stemcell-[0-9]+\\.[0-9]+-gdch-kvm-ubuntu-noble\\.tgz$"))' 2>/dev/null |
    sort -V | tail -n 1 || true
}

resolve_latest_stemcell_version() {
  if [[ -n "${STEMCELL_VERSION:-}" && "${STEMCELL_VERSION}" != "latest" ]]; then
    return 0
  fi
  echo "[INFO] Resolving latest GDC Ubuntu Noble stemcell version from gs://${STEMCELL_CANDIDATE_BUCKET}/gdch/..."
  local latest_obj latest_ver=""
  latest_obj="$(resolve_latest_stemcell_object)"
  if [[ "${latest_obj}" =~ ^gdch/bosh-stemcell-([0-9]+\.[0-9]+)-gdch-kvm-ubuntu-noble\.tgz$ ]]; then
    latest_ver="${BASH_REMATCH[1]}"
  fi
  if [[ -z "${latest_ver}" && -n "${STEMCELL_BUILDER_REPO_URL:-}" ]]; then
    local branch="${STEMCELL_BUILDER_BRANCH:-ubuntu-noble}"
    echo "[INFO] Falling back to latest ${branch}/v* git tag from ${STEMCELL_BUILDER_REPO_URL}..."
    latest_ver="$(git ls-remote --tags --refs "${STEMCELL_BUILDER_REPO_URL}" "refs/tags/${branch}/v*" 2>/dev/null |
      awk -v prefix="refs/tags/${branch}/v" '{sub(prefix, "", $2); print $2}' |
      sort -V | tail -n 1 || true)"
  fi
  if [[ -z "${latest_ver}" ]]; then
    echo "[ERROR] Unable to resolve latest GDC Ubuntu Noble stemcell version." >&2
    exit 1
  fi
  STEMCELL_VERSION="${latest_ver}"
  echo "[INFO] Resolved latest GDC Ubuntu Noble stemcell version: ${STEMCELL_VERSION}"
}

download_noble_stemcell() {
  local dest_dir="${1:-noble-stemcells}"
  local url_file="${2:-cicd-deployment-artifacts/noble-stemcells/url}"
  local noble_stemcell_obj=""

  mkdir -p "${dest_dir}" "$(dirname "${url_file}")"

  if [[ -s "${url_file}" ]]; then
    noble_stemcell_obj="$(sed -E "s|^gs://${STEMCELL_CANDIDATE_BUCKET}/||; s|^${STEMCELL_CANDIDATE_BASE_URL}/||" "${url_file}" | tr -d '[:space:]')"
  fi
  if [[ -z "${noble_stemcell_obj}" ]]; then
    noble_stemcell_obj="$(resolve_latest_stemcell_object)"
  fi
  if [[ -z "${noble_stemcell_obj}" ]]; then
    echo "[ERROR] Failed to resolve latest GDC Ubuntu Noble stemcell from gs://${STEMCELL_CANDIDATE_BUCKET}/gdch/" >&2
    exit 1
  fi

  noble_stemcell_tarball="$(basename "${noble_stemcell_obj}")"
  echo "[INFO] Downloading GDC Ubuntu Noble stemcell ${noble_stemcell_tarball} from gs://${STEMCELL_CANDIDATE_BUCKET}/${noble_stemcell_obj}..."
  curl -fSL --retry 3 "${STEMCELL_CANDIDATE_BASE_URL}/${noble_stemcell_obj}" -o "${dest_dir}/${noble_stemcell_tarball}"
  echo "gs://${STEMCELL_CANDIDATE_BUCKET}/${noble_stemcell_obj}" >"${url_file}"
  export noble_stemcell_tarball
}

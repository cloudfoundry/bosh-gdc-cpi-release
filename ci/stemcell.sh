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

# Shared helper functions for discovering and downloading official published GDC
# Ubuntu Noble stemcells from bosh.io (gs://bosh-core-stemcells/).

STEMCELL_NAME="bosh-gdch-kvm-ubuntu-noble"
STEMCELL_BOSH_IO_API_URL="https://bosh.io/api/v1/stemcells/${STEMCELL_NAME}"
STEMCELL_BUCKET="bosh-core-stemcells"
STEMCELL_GCS_API_URL="https://storage.googleapis.com/storage/v1/b/${STEMCELL_BUCKET}/o?matchGlob=*/bosh-stemcell-*-gdch-kvm-ubuntu-noble.tgz&maxResults=1000"
STEMCELL_BASE_URL="https://storage.googleapis.com/${STEMCELL_BUCKET}"

resolve_published_stemcell_versions() {
  local versions=""
  versions="$(curl -fsSL --retry 3 "${STEMCELL_BOSH_IO_API_URL}" 2>/dev/null |
    jq -r --arg name "${STEMCELL_NAME}" '.[]? | select(.name == $name) | .version // empty | select(test("^[0-9]+\\.[0-9]+$"))' 2>/dev/null |
    sort -V || true)"
  if [[ -z "${versions}" ]]; then
    versions="$(curl -fsSL --retry 3 "${STEMCELL_GCS_API_URL}" 2>/dev/null |
      jq -r '.items[]?.name // empty | capture("^(?<v>[0-9]+\\.[0-9]+)/bosh-stemcell-(?<v2>[0-9]+\\.[0-9]+)-gdch-kvm-ubuntu-noble\\.tgz$") | select(.v == .v2) | .v' 2>/dev/null |
      sort -V || true)"
  fi
  printf '%s\n' "${versions}" | sed '/^$/d'
}

resolve_published_stemcell_sha256() {
  local version="$1"
  curl -fsSL --retry 3 "${STEMCELL_BOSH_IO_API_URL}" 2>/dev/null |
    jq -r --arg name "${STEMCELL_NAME}" --arg ver "${version}" '.[]? | select(.name == $name and .version == $ver) | .regular.sha256 // empty' 2>/dev/null |
    head -n 1 || true
}

resolve_latest_stemcell_object() {
  local latest_ver=""
  latest_ver="$(resolve_published_stemcell_versions | tail -n 1)"
  if [[ -n "${latest_ver}" ]]; then
    printf '%s/bosh-stemcell-%s-gdch-kvm-ubuntu-noble.tgz\n' "${latest_ver}" "${latest_ver}"
  fi
}

resolve_latest_stemcell_version() {
  if [[ -n "${STEMCELL_VERSION:-}" && "${STEMCELL_VERSION}" != "latest" ]]; then
    return 0
  fi
  echo "[INFO] Resolving latest published GDC Ubuntu Noble stemcell version from bosh.io (${STEMCELL_BOSH_IO_API_URL})..."
  local latest_ver=""
  latest_ver="$(resolve_published_stemcell_versions | tail -n 1)"
  if [[ -z "${latest_ver}" ]]; then
    echo "[ERROR] Unable to resolve latest published GDC Ubuntu Noble stemcell version from bosh.io or gs://${STEMCELL_BUCKET}/." >&2
    exit 1
  fi
  STEMCELL_VERSION="${latest_ver}"
  echo "[INFO] Resolved latest published GDC Ubuntu Noble stemcell version: ${STEMCELL_VERSION}"
}

download_noble_stemcell() {
  local dest_dir="${1:-noble-stemcells}"
  local url_file="${2:-cicd-deployment-artifacts/noble-stemcells/url}"
  local noble_stemcell_obj=""
  local stemcell_ver=""

  mkdir -p "${dest_dir}" "$(dirname "${url_file}")"

  if [[ -s "${url_file}" ]]; then
    local recorded_uri
    recorded_uri="$(tr -d '[:space:]' <"${url_file}")"
    if [[ "${recorded_uri}" == "gs://${STEMCELL_BUCKET}/"* || "${recorded_uri}" == "${STEMCELL_BASE_URL}/"* ]]; then
      noble_stemcell_obj="$(printf '%s' "${recorded_uri}" | sed -E "s|^gs://${STEMCELL_BUCKET}/||; s|^${STEMCELL_BASE_URL}/||")"
    elif [[ -n "${recorded_uri}" ]]; then
      echo "[WARN] Ignoring non-published stemcell URI '${recorded_uri}' in ${url_file}; resolving published release from bosh.io instead."
    fi
  fi
  if [[ -z "${noble_stemcell_obj}" && -n "${STEMCELL_VERSION:-}" && "${STEMCELL_VERSION}" != "latest" ]]; then
    noble_stemcell_obj="${STEMCELL_VERSION}/bosh-stemcell-${STEMCELL_VERSION}-gdch-kvm-ubuntu-noble.tgz"
  fi
  if [[ -z "${noble_stemcell_obj}" ]]; then
    noble_stemcell_obj="$(resolve_latest_stemcell_object)"
  fi
  if [[ ! "${noble_stemcell_obj}" =~ ^([0-9]+\.[0-9]+)/bosh-stemcell-([0-9]+\.[0-9]+)-gdch-kvm-ubuntu-noble\.tgz$ ]] || [[ "${BASH_REMATCH[1]}" != "${BASH_REMATCH[2]}" ]]; then
    echo "[ERROR] Failed to resolve a valid published GDC Ubuntu Noble stemcell in gs://${STEMCELL_BUCKET}/ (got '${noble_stemcell_obj}')." >&2
    exit 1
  fi
  stemcell_ver="${BASH_REMATCH[1]}"

  noble_stemcell_tarball="$(basename "${noble_stemcell_obj}")"
  echo "[INFO] Downloading published GDC Ubuntu Noble stemcell ${noble_stemcell_tarball} from gs://${STEMCELL_BUCKET}/${noble_stemcell_obj}..."
  curl -fSL --retry 3 "${STEMCELL_BASE_URL}/${noble_stemcell_obj}" -o "${dest_dir}/${noble_stemcell_tarball}"

  local expected_sha256=""
  expected_sha256="$(resolve_published_stemcell_sha256 "${stemcell_ver}")"
  if [[ -n "${expected_sha256}" ]] && command -v sha256sum >/dev/null 2>&1; then
    local actual_sha256
    actual_sha256="$(sha256sum "${dest_dir}/${noble_stemcell_tarball}" | awk '{print $1}')"
    if [[ "${actual_sha256}" != "${expected_sha256}" ]]; then
      echo "[ERROR] SHA-256 mismatch for ${noble_stemcell_tarball}: expected ${expected_sha256}, got ${actual_sha256}." >&2
      rm -f "${dest_dir}/${noble_stemcell_tarball}"
      exit 1
    fi
    echo "[INFO] Verified SHA-256 digest for ${noble_stemcell_tarball}: ${actual_sha256}"
  fi

  echo "gs://${STEMCELL_BUCKET}/${noble_stemcell_obj}" >"${url_file}"
  export noble_stemcell_tarball
}

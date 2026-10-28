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
#
# Orchestrate SemVer resolution, candidate CPI build & vulnerability scanning,
# and final GitHub + GCS release publishing for bosh-gdc-cpi-release.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

STAGE=""
RELEASE_MODE="${RELEASE_MODE:-snapshot}"
INPUT_VERSION="${INPUT_VERSION:-}"
NEXT_VERSION_MODE="${NEXT_VERSION_MODE:-bump-patch}"
ALLOW_BRANCH="${ALLOW_BRANCH:-false}"
CONCOURSE_STATUS="${CONCOURSE_STATUS:-succeeded}"
BETA_GCS_BUCKET="${BETA_GCS_BUCKET:-gs://cfbosh-cicd}"
PROD_GCS_BUCKET="${PROD_GCS_BUCKET:-gs://gdc-external-sap}"
UPLOAD_TO_GCS="${UPLOAD_TO_GCS:-true}"
PUBLISH_GITHUB_RELEASE="${PUBLISH_GITHUB_RELEASE:-true}"
FORCE_RELEASE="${FORCE_RELEASE:-false}"
# Commit releases/ + .final_builds/ (bosh.io final-release metadata) back to this branch
PUSH_FINAL_RELEASE_METADATA="${PUSH_FINAL_RELEASE_METADATA:-${GITHUB_ACTIONS:-false}}"
FINAL_RELEASE_METADATA_BRANCH="${FINAL_RELEASE_METADATA_BRANCH:-main}"

TMP_DIR="$(mktemp -d -t release-cpi-XXXXXX)"
cleanup() {
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

usage() {
  cat <<EOF
Usage: $0 --stage <resolve|candidate|publish> [options]

Options:
  --stage <name>            Pipeline stage: resolve | candidate | publish
  --mode <snapshot|release> Execution mode (default: snapshot; release = official SemVer release)
  --version <X.Y.Z>         Explicit SemVer version override
  --next-version <mode>     Version bump mode: bump-patch | bump-minor | bump-major | noop
  --concourse-status <s>    Status of Concourse stage: succeeded | skipped | failed
  --allow-branch            Allow running from a non-main branch (snapshot mode only)
  --no-gcs                  Skip uploading artifacts to GCS buckets
  --no-github-release       Skip publishing GitHub Release
  --force-release           Force release publishing even if no new commits exist since latest tag
  --no-push-metadata        Do not commit/push releases/ and .final_builds/ (bosh.io metadata)
  -h, --help                Show this help message
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --stage)
    STAGE="$2"
    shift 2
    ;;
  --mode)
    RELEASE_MODE="$2"
    shift 2
    ;;
  --version)
    INPUT_VERSION="$2"
    shift 2
    ;;
  --next-version)
    NEXT_VERSION_MODE="$2"
    shift 2
    ;;
  --concourse-status)
    CONCOURSE_STATUS="$2"
    shift 2
    ;;
  --allow-branch)
    ALLOW_BRANCH="true"
    shift
    ;;
  --no-gcs)
    UPLOAD_TO_GCS="false"
    shift
    ;;
  --no-github-release)
    PUBLISH_GITHUB_RELEASE="false"
    shift
    ;;
  --force-release)
    FORCE_RELEASE="true"
    shift
    ;;
  --no-push-metadata)
    PUSH_FINAL_RELEASE_METADATA="false"
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
  if [[ "${UPLOAD_TO_GCS}" != "true" ]]; then
    return 0
  fi
  if [[ -n "${CFBOSH_CICD_SERVICE_ACCOUNT_KEY:-}" ]]; then
    echo "[INFO] Authenticating GCP service account..."
    local sa_key_file="${TMP_DIR}/gcp-sa-key.json"
    umask 077
    printf '%s' "${CFBOSH_CICD_SERVICE_ACCOUNT_KEY}" >"${sa_key_file}"
    gcloud auth activate-service-account --key-file="${sa_key_file}" --quiet >/dev/null 2>&1
    export GOOGLE_APPLICATION_CREDENTIALS="${sa_key_file}"
  fi
}

ensure_bosh_cli() {
  mkdir -p "${HOME}/.local/bin"
  export PATH="${HOME}/.local/bin:${PATH}"
  if ! command -v bosh >/dev/null 2>&1; then
    echo "[INFO] Installing BOSH CLI v7.8.2..."
    curl -fsSL "https://github.com/cloudfoundry/bosh-cli/releases/download/v7.8.2/bosh-cli-7.8.2-linux-amd64" -o "${HOME}/.local/bin/bosh"
    chmod +x "${HOME}/.local/bin/bosh"
  fi
}

resolve_version() {
  # Ensure all release tags are fetched so next_version_from_tags sees the latest vX.Y.Z tag
  git fetch --tags --force origin 2>/dev/null || true

  local raw_version="${INPUT_VERSION}"
  if [[ -z "${raw_version}" ]]; then
    raw_version="$(python3 "${SCRIPT_DIR}/_release_tag.py" --repo "${REPO_ROOT}" --resolve-next "${NEXT_VERSION_MODE}")"
  fi

  # Strip leading 'v' and trailing '-dev' to get base X.Y.Z
  local base_version="${raw_version#v}"
  base_version="${base_version%-dev}"

  # Normalize legacy 'candidate' mode to 'snapshot'
  if [[ "${RELEASE_MODE}" == "candidate" ]]; then
    RELEASE_MODE="snapshot"
  fi

  local head_sha short_sha
  head_sha="$(git rev-parse HEAD)"
  short_sha="$(git rev-parse --short=7 HEAD)"

  local release_version="${base_version}"
  if [[ "${RELEASE_MODE}" == "snapshot" && "${release_version}" != *-dev.* && "${release_version}" != *-snapshot.* && "${release_version}" != *-rc* ]]; then
    release_version="${base_version}-dev.${short_sha}"
  fi

  local release_tag="v${release_version}"

  local tag_args=(
    "--repo" "${REPO_ROOT}"
    "--tag" "${release_tag}"
    "--sha" "${head_sha}"
  )
  if [[ "${ALLOW_BRANCH}" == "true" || "${RELEASE_MODE}" == "snapshot" ]]; then
    tag_args+=("--allow-branch")
  fi

  local plan_file="${TMP_DIR}/release-plan.env"
  python3 "${SCRIPT_DIR}/_release_tag.py" "${tag_args[@]}" --github-output "${plan_file}"
  python3 "${SCRIPT_DIR}/_release_tag.py" --repo "${REPO_ROOT}" --check-new-commits --sha "${head_sha}" --github-output "${plan_file}"

  # shellcheck disable=SC1090
  source "${plan_file}"

  echo "[INFO] Resolved SemVer release plan (derived from Git tags):"
  echo "  Mode:             ${RELEASE_MODE}"
  echo "  Base Version:     ${base_version}"
  echo "  Release Version:  ${version}"
  echo "  Release Tag:      ${release_tag}"
  echo "  Prerelease:       ${prerelease}"
  echo "  Latest:           ${latest}"
  echo "  Source:           ${source}"
  echo "  Latest Final Tag: ${latest_tag:-none}"
  echo "  New Commits:      ${commits_since_tag} (has_new_commits=${has_new_commits})"
  if [[ "${has_new_commits}" == "false" && "${FORCE_RELEASE}" != "true" ]]; then
    echo "[INFO] No new commits since latest release tag '${latest_tag}'; release artifact publishing will be skipped."
  fi

  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "release_mode=${RELEASE_MODE}"
      echo "base_version=${base_version}"
      echo "version=${version}"
      echo "tag=${release_tag}"
      echo "prerelease=${prerelease}"
      echo "latest=${latest}"
      echo "source=${source}"
      echo "latest_tag=${latest_tag}"
      echo "commits_since_tag=${commits_since_tag}"
      echo "has_new_commits=${has_new_commits}"
    } >>"${GITHUB_OUTPUT}"
  fi
}

generate_vuln_report() {
  local version="$1"
  local report_path="$2"
  local binary_path="${REPO_ROOT}/src/bosh-gdc-cpi/cpi_binary_linux"

  if [[ ! -f "${binary_path}" ]]; then
    make release-binaries VERSION="${version}"
  fi

  if [[ -d "${REPO_ROOT}/.tools/go/bin" ]]; then
    export GOROOT="${REPO_ROOT}/.tools/go"
    export PATH="${REPO_ROOT}/.tools/go/bin:${REPO_ROOT}/.tools/bin:${PATH}"
  fi

  echo "[INFO] Running govulncheck against compiled BOSH CPI binary (version: ${version})..."
  local json_output="${TMP_DIR}/govulncheck.json"
  if command -v govulncheck >/dev/null 2>&1; then
    govulncheck -mode=binary -json "${binary_path}" >"${json_output}" || true
  else
    go run golang.org/x/vuln/cmd/govulncheck@latest -mode=binary -json "${binary_path}" >"${json_output}" || true
  fi

  {
    echo "### Vulnerability Report for BOSH CPI"
    echo "**Version:** ${version}"
    echo "**Scan Date:** $(date -u '+%Y-%m-%d %H:%M:%S UTC')"
    echo ""
    echo "| Code | Link |"
    echo "| :--- | :--- |"

    local vulnerabilities
    vulnerabilities="$(jq -r '.finding.osv? // empty' "${json_output}" 2>/dev/null | sort -u || true)"
    if [[ -n "${vulnerabilities}" ]]; then
      while read -r id; do
        [[ -z "${id}" ]] && continue
        echo "| ${id} | [${id}](https://pkg.go.dev/vuln/${id}) |"
      done <<<"${vulnerabilities}"
    else
      echo "| None | No vulnerabilities found |"
    fi
  } >"${report_path}"

  echo "[INFO] Vulnerability scan report generated at ${report_path}:"
  cat "${report_path}"
}

build_concourse_tarball() {
  local version="$1"
  local concourse_tarball="$2"
  local stage_dir="${TMP_DIR}/concourse-bosh-release"

  echo "[INFO] Building Concourse-compatible CPI release tarball (release: bosh-gdch-cpi, job: gdch_cpi, version: ${version})..."
  rm -rf "${stage_dir}"
  mkdir -p "${stage_dir}/config" \
    "${stage_dir}/jobs/gdch_cpi/templates" \
    "${stage_dir}/packages/bosh-gdch-cpi" \
    "${stage_dir}/src/bosh-gdch-cpi"

  printf -- '---\nname: bosh-gdch-cpi\n' >"${stage_dir}/config/final.yml"
  cp "${REPO_ROOT}/config/blobs.yml" "${stage_dir}/config/blobs.yml"
  if [[ -f "${REPO_ROOT}/LICENSE" ]]; then
    cp "${REPO_ROOT}/LICENSE" "${stage_dir}/LICENSE"
  fi

  cp "${REPO_ROOT}/jobs/gdc_cpi/monit" "${stage_dir}/jobs/gdch_cpi/monit"
  cp "${REPO_ROOT}/jobs/gdc_cpi/templates/cpi_config.json.erb" "${stage_dir}/jobs/gdch_cpi/templates/cpi_config.json.erb"
  sed -e 's/gdc_cpi/gdch_cpi/g' -e 's/bosh-gdc-cpi/bosh-gdch-cpi/g' \
    "${REPO_ROOT}/jobs/gdc_cpi/spec" >"${stage_dir}/jobs/gdch_cpi/spec"
  sed -e 's/gdc_cpi/gdch_cpi/g' -e 's/bosh-gdc-cpi/bosh-gdch-cpi/g' \
    "${REPO_ROOT}/jobs/gdc_cpi/templates/cpi.erb" >"${stage_dir}/jobs/gdch_cpi/templates/cpi.erb"
  chmod +x "${stage_dir}/jobs/gdch_cpi/templates/cpi.erb"

  sed 's/bosh-gdc-cpi/bosh-gdch-cpi/g' \
    "${REPO_ROOT}/packages/bosh-gdc-cpi/spec" >"${stage_dir}/packages/bosh-gdch-cpi/spec"
  sed 's/bosh-gdc-cpi/bosh-gdch-cpi/g' \
    "${REPO_ROOT}/packages/bosh-gdc-cpi/packaging" >"${stage_dir}/packages/bosh-gdch-cpi/packaging"
  chmod +x "${stage_dir}/packages/bosh-gdch-cpi/packaging"

  cp "${REPO_ROOT}/src/bosh-gdc-cpi/"* "${stage_dir}/src/bosh-gdch-cpi/"

  bosh create-release --force \
    --dir="${stage_dir}" \
    --name="bosh-gdch-cpi" \
    --version="${version}" \
    --tarball="${concourse_tarball}"

  python3 "${SCRIPT_DIR}/_release_artifact.py" "${concourse_tarball}" \
    --expected-name "bosh-gdch-cpi" \
    --expected-version "${version}" >/dev/null
}

build_candidate_stage() {
  ensure_bosh_cli
  setup_gcp_auth

  local version="${INPUT_VERSION}"
  if [[ -z "${version}" ]]; then
    echo "[ERROR] --version is required for --stage candidate" >&2
    exit 1
  fi

  echo "[INFO] Building BOSH CPI release tarball for version ${version}..."
  "${SCRIPT_DIR}/create-release" "${version}"

  local release_dir="${REPO_ROOT}/releases/bosh-gdc-cpi"
  local semver_tarball="${release_dir}/bosh-gdc-cpi-${version}.tgz"
  local semver_sha256_file="${semver_tarball}.sha256"
  local concourse_tarball="${release_dir}/bosh_gdch_cpi_release_${version}.tgz"
  local vuln_report="${release_dir}/vulnerability-report-${version}.md"

  # Maintain Concourse GCS resource compatibility: bosh-deployment on Concourse expects
  # release.MF inside bosh_gdch_cpi_release_<version>.tgz to have name: bosh-gdch-cpi and job: gdch_cpi.
  build_concourse_tarball "${version}" "${concourse_tarball}"

  # Verify archive integrity and export GDC_CPI_RELEASE_* env vars
  local identity_json
  identity_json="$(python3 "${SCRIPT_DIR}/_release_artifact.py" "${semver_tarball}" \
    --expected-version "${version}" \
    ${GITHUB_ENV:+--github-env "${GITHUB_ENV}"})"
  local sha256
  sha256="$(echo "${identity_json}" | jq -r '.sha256')"

  generate_vuln_report "${version}" "${vuln_report}"

  if [[ "${UPLOAD_TO_GCS}" == "true" ]]; then
    local concourse_ts_version
    concourse_ts_version="$(date -u +%Y%m%d-%H%M%S)-${version}"
    echo "[INFO] Uploading candidate CPI artifacts to ${BETA_GCS_BUCKET}/cpi_releases/..."
    gcloud storage cp "${concourse_tarball}" "${BETA_GCS_BUCKET}/cpi_releases/bosh_gdch_cpi_release_${version}.tgz"
    gcloud storage cp "${concourse_tarball}" "${BETA_GCS_BUCKET}/cpi_releases/bosh_gdch_cpi_release_${concourse_ts_version}.tgz"
    gcloud storage cp "${semver_tarball}" "${BETA_GCS_BUCKET}/cpi_releases/bosh-gdc-cpi-${version}.tgz"
    gcloud storage cp "${semver_sha256_file}" "${BETA_GCS_BUCKET}/cpi_releases/bosh-gdc-cpi-${version}.tgz.sha256"

    echo "[INFO] Uploading CPI vulnerability report to ${BETA_GCS_BUCKET}/vulnerability-reports/cpi/${version}.md..."
    gcloud storage cp --content-type="text/markdown" "${vuln_report}" "${BETA_GCS_BUCKET}/vulnerability-reports/cpi/${version}.md"
  fi

  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "tarball_path=${semver_tarball}"
      echo "concourse_tarball_path=${concourse_tarball}"
      echo "sha256_path=${semver_sha256_file}"
      echo "vuln_report_path=${vuln_report}"
      echo "sha256=${sha256}"
      echo "beta_gcs_uri=${BETA_GCS_BUCKET}/cpi_releases/bosh_gdch_cpi_release_${version}.tgz"
    } >>"${GITHUB_OUTPUT}"
  fi
}

generate_release_notes() {
  local repo="$1"
  local tag="$2"
  local previous_tag="${3:-}"

  local -a api_args=("-f" "tag_name=${tag}" "-f" "target_commitish=$(git rev-parse HEAD)")
  if [[ -n "${previous_tag}" && "${previous_tag}" != "${tag}" ]]; then
    api_args+=("-f" "previous_tag_name=${previous_tag}")
  fi

  local body
  if ! body="$(gh api --method POST "repos/${repo}/releases/generate-notes" "${api_args[@]}" --jq '.body' 2>/dev/null)"; then
    echo "[WARN] Could not auto-generate release notes for ${tag} (previous: ${previous_tag:-auto}); continuing without 'What's Changed' section." >&2
    return 1
  fi
  if [[ -z "${body}" ]]; then
    return 1
  fi
  printf '\n%s\n' "${body}"
}

commit_final_release_metadata() {
  local version="$1"
  local tag="$2"

  git add -- .final_builds "releases/bosh-gdc-cpi/index.yml" "releases/bosh-gdc-cpi/bosh-gdc-cpi-${version}.yml"
  if git diff --cached --quiet; then
    echo "[INFO] Final release metadata for ${version} already committed; nothing to push."
    return 0
  fi

  if [[ "${PUSH_FINAL_RELEASE_METADATA}" != "true" ]]; then
    echo "[INFO] Final release metadata for ${version} is staged but not committed (PUSH_FINAL_RELEASE_METADATA=${PUSH_FINAL_RELEASE_METADATA})."
    echo "       Commit releases/ and .final_builds/ so bosh.io can import ${tag}."
    return 0
  fi

  if [[ -z "$(git config user.email)" ]]; then
    git config user.name "github-actions[bot]"
    git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
  fi
  git commit -q -s -m "chore(release): cut bosh-gdc-cpi ${tag}" \
    -m "Final release metadata (releases/, .final_builds/) for bosh.io; blobs are served from the ${tag} GitHub Release assets."
  echo "[INFO] Committed final release metadata for ${tag} ($(git rev-parse --short HEAD))."

  local branch="${FINAL_RELEASE_METADATA_BRANCH}"
  local attempt
  for attempt in 1 2 3; do
    if git push origin "HEAD:refs/heads/${branch}"; then
      echo "[INFO] Pushed final release metadata for ${tag} to ${branch}."
      return 0
    fi
    echo "[WARN] Push to ${branch} rejected (attempt ${attempt}/3); rebasing onto latest ${branch}..."
    git fetch origin "${branch}"
    git rebase "origin/${branch}" || {
      git rebase --abort || true
      break
    }
  done
  echo "[ERROR] Could not push final release metadata for ${tag} to ${branch}. Push the commit manually so bosh.io can import the release." >&2
  return 1
}

publish_release_stage() {
  setup_gcp_auth

  local version="${INPUT_VERSION}"
  if [[ -z "${version}" ]]; then
    echo "[ERROR] --version is required for --stage publish" >&2
    exit 1
  fi

  local release_dir="${REPO_ROOT}/releases/bosh-gdc-cpi"
  local semver_tarball="${release_dir}/bosh-gdc-cpi-${version}.tgz"
  local semver_sha256_file="${semver_tarball}.sha256"
  local concourse_tarball="${release_dir}/bosh_gdch_cpi_release_${version}.tgz"
  local vuln_report="${release_dir}/vulnerability-report-${version}.md"

  if [[ ! -f "${semver_tarball}" ]]; then
    echo "[INFO] Tarball not found in workspace; building ${semver_tarball}..."
    ensure_bosh_cli
    "${SCRIPT_DIR}/create-release" "${version}"
  fi
  if [[ ! -f "${concourse_tarball}" ]]; then
    ensure_bosh_cli
    build_concourse_tarball "${version}" "${concourse_tarball}"
  fi
  if [[ ! -f "${vuln_report}" ]]; then
    generate_vuln_report "${version}" "${vuln_report}"
  fi

  local expected_sha256=""
  if [[ -f "${semver_sha256_file}" ]]; then
    expected_sha256="$(awk '{print $1}' "${semver_sha256_file}")"
  fi

  local identity_json
  identity_json="$(python3 "${SCRIPT_DIR}/_release_artifact.py" "${semver_tarball}" \
    --sha256 "${expected_sha256}" \
    --expected-version "${version}")"
  local sha256
  sha256="$(echo "${identity_json}" | jq -r '.sha256')"

  # Normalize legacy 'candidate' mode to 'snapshot'
  if [[ "${RELEASE_MODE}" == "candidate" ]]; then
    RELEASE_MODE="snapshot"
  fi

  # Skip publishing if there are no new commits since the latest final release tag
  git fetch --tags --force origin 2>/dev/null || true
  local commit_check_file="${TMP_DIR}/commit-check.env"
  python3 "${SCRIPT_DIR}/_release_tag.py" --repo "${REPO_ROOT}" --check-new-commits --sha HEAD --github-output "${commit_check_file}"
  # shellcheck disable=SC1090
  source "${commit_check_file}"

  if [[ "${has_new_commits:-true}" == "false" && "${FORCE_RELEASE}" != "true" ]]; then
    echo "[INFO] No new commits found between latest release tag '${latest_tag}' and current HEAD ($(git rev-parse --short HEAD)); skipping release artifact publishing to GCS and GitHub Releases."
    if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
      {
        echo "release_skipped=true"
        echo "release_url="
        echo "prod_gcs_uri=N/A (skipped — no new commits since ${latest_tag})"
        echo "sha256=${sha256}"
      } >>"${GITHUB_OUTPUT}"
    fi
    return 0
  fi

  local published_gcs_bucket="${BETA_GCS_BUCKET}"
  if [[ "${UPLOAD_TO_GCS}" == "true" ]]; then
    echo "[INFO] Publishing CPI artifacts (${RELEASE_MODE} mode) to ${BETA_GCS_BUCKET}/cpi_releases/..."
    gcloud storage cp "${semver_tarball}" "${BETA_GCS_BUCKET}/cpi_releases/bosh-gdc-cpi-${version}.tgz"
    gcloud storage cp "${semver_sha256_file}" "${BETA_GCS_BUCKET}/cpi_releases/bosh-gdc-cpi-${version}.tgz.sha256"
    gcloud storage cp "${concourse_tarball}" "${BETA_GCS_BUCKET}/cpi_releases/bosh_gdch_cpi_release_${version}.tgz"
    gcloud storage cp --content-type="text/markdown" "${vuln_report}" "${BETA_GCS_BUCKET}/vulnerability-reports/cpi/${version}.md"
  fi

  if [[ "${RELEASE_MODE}" != "release" ]]; then
    echo "[INFO] Snapshot mode (${version}): skipping external GitHub Release, git tagging, and external GCS promotion."
    if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
      {
        echo "release_url="
        echo "prod_gcs_uri=${BETA_GCS_BUCKET}/cpi_releases/bosh-gdc-cpi-${version}.tgz"
        echo "sha256=${sha256}"
      } >>"${GITHUB_OUTPUT}"
    fi
    return 0
  fi

  if [[ "${GITHUB_EVENT_NAME:-}" == "pull_request" && "${ALLOW_PR_RELEASE:-false}" != "true" ]]; then
    echo "[ERROR] Refusing to publish an official external release from a Pull Request label trigger (event=${GITHUB_EVENT_NAME}). Use --mode snapshot instead." >&2
    exit 1
  fi

  if [[ "${UPLOAD_TO_GCS}" == "true" ]]; then
    echo "[INFO] Mirroring official CPI release artifacts to external bucket ${PROD_GCS_BUCKET}/cpi_releases/ (if permitted)..."
    if gcloud storage cp "${semver_tarball}" "${PROD_GCS_BUCKET}/cpi_releases/bosh-gdc-cpi-${version}.tgz" 2>/dev/null; then
      gcloud storage cp "${semver_sha256_file}" "${PROD_GCS_BUCKET}/cpi_releases/bosh-gdc-cpi-${version}.tgz.sha256" || true
      gcloud storage cp "${concourse_tarball}" "${PROD_GCS_BUCKET}/cpi_releases/bosh_gdch_cpi_release_${version}.tgz" || true
      gcloud storage cp --content-type="text/markdown" "${vuln_report}" "${PROD_GCS_BUCKET}/vulnerability-reports/cpi/${version}.md" || true
      published_gcs_bucket="${PROD_GCS_BUCKET}"
    else
      echo "[WARN] Service account does not have write access to ${PROD_GCS_BUCKET}; published to ${BETA_GCS_BUCKET} and GitHub Releases."
    fi
  fi

  local tag="v${version}"
  local repo="${GITHUB_REPOSITORY:-cloudfoundry/bosh-gdc-cpi-release}"
  local download_url="https://github.com/${repo}/releases/download/${tag}/bosh-gdc-cpi-${version}.tgz"
  local notes_file="${TMP_DIR}/release-notes.md"

  # bosh.io ignores GitHub Releases: it rebuilds final releases from the committed
  # releases/<name>/<name>-<version>.yml + .final_builds/ metadata, downloading every blob
  # from the blobstore in config/final.yml (GitHub Release assets of this tag).
  local final_assets_dir="${TMP_DIR}/final-release-assets"
  local final_release_json="${TMP_DIR}/final-release.json"
  local blob_assets=()
  if [[ -f "${REPO_ROOT}/releases/bosh-gdc-cpi/bosh-gdc-cpi-${version}.yml" ]]; then
    echo "[INFO] Final release metadata for ${version} already committed; skipping finalize-release."
  else
    ensure_bosh_cli
    echo "[INFO] Generating bosh.io final release metadata (releases/, .final_builds/) for ${version}..."
    python3 "${SCRIPT_DIR}/_final_release.py" --repo "${REPO_ROOT}" finalize \
      --tarball "${semver_tarball}" \
      --version "${version}" \
      --assets-dir "${final_assets_dir}" \
      --json "${final_release_json}"
    mapfile -t blob_assets < <(jq -r '.assets[].path' "${final_release_json}")
    echo "[INFO] Staged ${#blob_assets[@]} final-build blob asset(s) for GitHub Release ${tag}."
  fi

  cat >"${notes_file}" <<EOF
## BOSH Google Distributed Cloud (GDC) CPI Release \`${tag}\`

### BOSH Deployment Manifest Snippet

\`\`\`yaml
releases:
  - name: bosh-gdc-cpi
    version: "${version}"
    url: ${download_url}
    sha1: sha256:${sha256}
\`\`\`

### Release Provenance & Verification

| Field | Value |
| :--- | :--- |
| **Release Name** | \`bosh-gdc-cpi\` |
| **Version** | \`${version}\` |
| **Git Tag** | \`${tag}\` |
| **Commit SHA** | \`$(git rev-parse --short HEAD)\` |
| **SHA-256 Digest** | \`${sha256}\` |
| **Concourse Stage** | \`${CONCOURSE_STATUS}\` |
| **GCS Release Bucket** | \`${published_gcs_bucket}/cpi_releases/bosh-gdc-cpi-${version}.tgz\` |

$(cat "${vuln_report}")
EOF

  # Append GitHub's auto-generated "What's Changed" / "Full Changelog" section (merged PRs,
  # new contributors, compare link since the previous final tag), as on other CPI releases.
  if [[ "${PUBLISH_GITHUB_RELEASE}" == "true" ]]; then
    generate_release_notes "${repo}" "${tag}" "${latest_tag:-}" >>"${notes_file}" || true
  fi

  if [[ "${PUBLISH_GITHUB_RELEASE}" == "true" ]]; then
    echo "[INFO] Publishing GitHub Release ${tag} on ${repo}..."
    local gh_flags=(
      "--repo" "${repo}"
      "--title" "BOSH GDC CPI ${tag}"
      "--notes-file" "${notes_file}"
    )

    if [[ "${RELEASE_MODE}" == "candidate" || "${version}" == *-* ]]; then
      gh_flags+=("--prerelease")
    elif [[ "${IS_LATEST:-true}" == "true" ]]; then
      gh_flags+=("--latest")
    else
      gh_flags+=("--latest=false")
    fi

    local release_assets=(
      "${semver_tarball}"
      "${semver_sha256_file}"
      "${concourse_tarball}"
      "${vuln_report}"
    )
    if [[ ${#blob_assets[@]} -gt 0 ]]; then
      release_assets+=("${blob_assets[@]}")
    fi

    if gh release view "${tag}" --repo "${repo}" >/dev/null 2>&1; then
      echo "[INFO] Updating existing GitHub Release ${tag}..."
      gh release upload "${tag}" \
        "${release_assets[@]}" \
        --repo "${repo}" \
        --clobber
      gh release edit "${tag}" "${gh_flags[@]}"
    else
      local target_sha
      target_sha="$(git rev-parse HEAD)"
      gh release create "${tag}" \
        "${release_assets[@]}" \
        "--target" "${target_sha}" \
        "${gh_flags[@]}"
    fi

    # Prove the committed metadata is self-sufficient: rebuild the final release the way
    # bosh.io does, from releases/<name>-<version>.yml with every blob fetched from GitHub.
    ensure_bosh_cli
    echo "[INFO] Verifying final release ${version} can be rebuilt from committed metadata + GitHub Release assets..."
    python3 "${SCRIPT_DIR}/_final_release.py" --repo "${REPO_ROOT}" verify \
      --version "${version}" \
      --tarball "${TMP_DIR}/bosh-gdc-cpi-${version}.rebuilt.tgz"
    python3 "${SCRIPT_DIR}/_release_artifact.py" "${TMP_DIR}/bosh-gdc-cpi-${version}.rebuilt.tgz" \
      --expected-version "${version}" >/dev/null
  fi

  commit_final_release_metadata "${version}" "${tag}"

  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "release_url=https://github.com/${repo}/releases/tag/${tag}"
      echo "prod_gcs_uri=${published_gcs_bucket}/cpi_releases/bosh-gdc-cpi-${version}.tgz"
      echo "sha256=${sha256}"
    } >>"${GITHUB_OUTPUT}"
  fi
}

case "${STAGE}" in
resolve)
  resolve_version
  ;;
candidate)
  build_candidate_stage
  ;;
publish)
  publish_release_stage
  ;;
*)
  echo "[ERROR] Invalid or missing --stage '${STAGE}'. Expected: resolve | candidate | publish" >&2
  usage
  exit 1
  ;;
esac

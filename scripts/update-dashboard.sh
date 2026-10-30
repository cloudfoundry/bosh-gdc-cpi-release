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
# Update dashboard/data/runs.json and generate GitHub Actions Step Summary
# for the CFBOSH GDC release pipeline.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

RUNS_JSON="${REPO_ROOT}/dashboard/data/runs.json"
mkdir -p "$(dirname "${RUNS_JSON}")"
if [[ ! -f "${RUNS_JSON}" ]]; then
  echo "[]" >"${RUNS_JSON}"
fi

if [[ "${HAS_NEW_COMMITS:-true}" == "false" && "${FORCE_RELEASE:-false}" != "true" ]]; then
  echo "[INFO] No new commits since latest release tag '${LATEST_TAG:-unknown}'; skipping dashboard update."
  exit 0
fi

END_EPOCH="$(date +%s)"
START_EPOCH="${PIPELINE_START_EPOCH:-${END_EPOCH}}"
DURATION_SEC=$((END_EPOCH - START_EPOCH))
if ((DURATION_SEC < 0)); then
  DURATION_SEC=0
fi
DURATION_MIN=$((DURATION_SEC / 60))
DURATION_REM=$((DURATION_SEC % 60))
DURATION_STR="${DURATION_MIN}m ${DURATION_REM}s"

map_stage_result() {
  local res="${1:-skipped}"
  case "${res}" in
  success) echo "passed" ;;
  failure | cancelled | timed_out) echo "failed" ;;
  *) echo "skipped" ;;
  esac
}

STAGE_BUILD="$(map_stage_result "${BUILD_CANDIDATE_RESULT:-skipped}")"
STAGE_CONCOURSE="$(map_stage_result "${CONCOURSE_RESULT:-skipped}")"
STAGE_VULN="${STAGE_BUILD}"
STAGE_RELEASE="$(map_stage_result "${RELEASE_RESULT:-skipped}")"

OVERALL_STATUS="${PIPELINE_OVERALL_STATUS:-}"
if [[ "${OVERALL_STATUS}" == "running" ]]; then
  STAGE_BUILD="running"
  STAGE_CONCOURSE="pending"
  STAGE_VULN="running"
  STAGE_RELEASE="pending"
elif [[ -z "${OVERALL_STATUS}" ]]; then
  OVERALL_STATUS="failure"
  if [[ "${BUILD_CANDIDATE_RESULT:-}" == "success" && "${RELEASE_RESULT:-}" == "success" ]]; then
    if [[ "${CONCOURSE_RESULT:-}" == "success" || ("${CONCOURSE_RESULT:-}" == "skipped" && "${SKIP_CONCOURSE:-false}" == "true") ]]; then
      OVERALL_STATUS="success"
    fi
  fi
fi

REPO="${GITHUB_REPOSITORY:-cloudfoundry/bosh-gdc-cpi-release}"
SERVER_URL="${GITHUB_SERVER_URL:-https://github.com}"
RUN_ID="${GITHUB_RUN_ID:-0}"
RUN_NUMBER="${GITHUB_RUN_NUMBER:-1}"
RUN_URL="${SERVER_URL}/${REPO}/actions/runs/${RUN_ID}"
RELEASE_VERSION="${RELEASE_VERSION:-0.1.0}"
RELEASE_TAG="v${RELEASE_VERSION#v}"
RELEASE_MODE_VAL="${RELEASE_MODE:-snapshot}"
if [[ "${RELEASE_MODE_VAL}" == "release" ]]; then
  RELEASE_URL="${SERVER_URL}/${REPO}/releases/tag/${RELEASE_TAG}"
  PROD_GCS_URI="${PROD_GCS_URI:-gs://cfbosh-cicd/cpi_releases/bosh-gdc-cpi-${RELEASE_VERSION#v}.tgz}"
else
  RELEASE_URL=""
  PROD_GCS_URI="N/A (snapshot mode — internal gs://cfbosh-cicd only)"
fi
COMMIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
COMMIT_URL="${SERVER_URL}/${REPO}/commit/${COMMIT_SHA}"
TIMESTAMP="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
TRIGGER="${GITHUB_EVENT_NAME:-manual}"
ACTOR="${GITHUB_ACTOR:-maintainer}"

GH_AUTH_HEADER=()
if [[ -n "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]]; then
  GH_AUTH_HEADER=(-H "Authorization: Bearer ${GH_TOKEN:-${GITHUB_TOKEN}}")
fi

# Merge the live runs.json from GitHub Pages (runs recorded by earlier pipeline runs that may
# not be committed yet) with the committed file, so neither writer can drop the other's runs.
# Live entries win on conflict. Then prune any runs that were deleted from GitHub Actions.
if curl -fsSL --max-time 10 "https://cloudfoundry.github.io/bosh-gdc-cpi-release/data/runs.json" -o "${RUNS_JSON}.remote" 2>/dev/null; then
  if jq -e 'type == "array"' "${RUNS_JSON}.remote" >/dev/null 2>&1; then
    jq -s '
      (.[1] + .[0])
      | group_by(.id | tostring)
      | map(sort_by(if .status == "failure" then 0 elif .status == "success" then 1 else 2 end) | .[0])
    ' "${RUNS_JSON}" "${RUNS_JSON}.remote" >"${RUNS_JSON}.merged" &&
      mv "${RUNS_JSON}.merged" "${RUNS_JSON}"
  fi
  rm -f "${RUNS_JSON}.remote"
fi

if curl -fsSL --max-time 10 "${GH_AUTH_HEADER[@]}" "https://api.github.com/repos/${REPO}/actions/workflows/release-pipeline.yaml/runs?per_page=100" -o "${RUNS_JSON}.gh" 2>/dev/null; then
  LIVE_IDS="$(jq -c '[.workflow_runs[]?.id | tostring]' "${RUNS_JSON}.gh" 2>/dev/null || echo '[]')"
  jq --argjson live "${LIVE_IDS}" --arg cur "${RUN_ID}" \
    '[.[] | select((.id | tostring) as $id | ($id == $cur) or ($live | index($id) != null))]' \
    "${RUNS_JSON}" >"${RUNS_JSON}.pruned" && mv "${RUNS_JSON}.pruned" "${RUNS_JSON}"

  # Synthesize live in-progress entries for any currently active workflow runs so
  # RECONCILE_ONLY deployments and concurrent runs immediately appear as "running".
  while IFS= read -r active_run_json; do
    [[ -z "${active_run_json}" ]] && continue
    active_id="$(echo "${active_run_json}" | jq -r '.id | tostring')"
    [[ "${active_id}" == "${RUN_ID}" && "${RECONCILE_ONLY:-false}" != "true" ]] && continue

    active_started="$(echo "${active_run_json}" | jq -r '.run_started_at // .created_at')"
    active_event="$(echo "${active_run_json}" | jq -r '.event // "workflow_dispatch"')"
    active_actor="$(echo "${active_run_json}" | jq -r '.actor.login // "maintainer"')"
    active_url="$(echo "${active_run_json}" | jq -r '.html_url')"
    active_sha="$(echo "${active_run_json}" | jq -r '.head_sha // ""' | cut -c1-7)"

    active_mode="snapshot"
    [[ "${active_event}" == "schedule" ]] && active_mode="release"
    active_ver="v0.1.1-dev.${active_sha}"
    s_build="running"
    s_concourse="pending"
    s_vuln="running"
    s_release="pending"

    if curl -fsSL --max-time 10 "${GH_AUTH_HEADER[@]}" "https://api.github.com/repos/${REPO}/actions/runs/${active_id}/artifacts" -o "${RUNS_JSON}.art" 2>/dev/null; then
      meta_name="$(jq -r '.artifacts[]?.name | select(startswith("pipeline-meta-"))' "${RUNS_JSON}.art" | head -n 1 || true)"
      if [[ "${meta_name}" == pipeline-meta-release-* ]]; then
        active_mode="release"
        active_ver="v${meta_name#pipeline-meta-release-}"
        active_ver="v${active_ver#v}"
      elif [[ "${meta_name}" == pipeline-meta-snapshot-* ]]; then
        active_mode="snapshot"
        active_ver="v${meta_name#pipeline-meta-snapshot-}"
        active_ver="v${active_ver#v}"
      fi
      if [[ "${active_ver}" =~ -dev\.([0-9a-f]{7,})$ ]]; then
        active_sha="${BASH_REMATCH[1]}"
      fi
      rm -f "${RUNS_JSON}.art"
    fi

    if curl -fsSL --max-time 10 "${GH_AUTH_HEADER[@]}" "https://api.github.com/repos/${REPO}/actions/runs/${active_id}/jobs" -o "${RUNS_JSON}.jobs" 2>/dev/null; then
      map_gh_job() {
        local pat="$1"
        jq -r --arg p "${pat}" '
          ([.jobs[]? | select(.name | contains($p))][0]) as $j
          | if $j == null then "pending"
            elif $j.status != "completed" then "running"
            elif $j.conclusion == "success" then "passed"
            elif $j.conclusion == "skipped" then "skipped"
            else "failed" end
        ' "${RUNS_JSON}.jobs"
      }
      s_build="$(map_gh_job "Build Candidate CPI")"
      s_concourse="$(map_gh_job "Run Concourse Deployment")"
      s_vuln="${s_build}"
      s_release="$(map_gh_job "Release CPI Artifact")"
      rm -f "${RUNS_JSON}.jobs"
    fi

    start_ts="$(date -d "${active_started}" +%s 2>/dev/null || echo "${END_EPOCH}")"
    el_sec=$((END_EPOCH - start_ts))
    ((el_sec < 0)) && el_sec=0
    el_str="$((el_sec / 60))m $((el_sec % 60))s (live)"

    active_entry="$(jq -n \
      --arg id "${active_id}" \
      --arg timestamp "${active_started}" \
      --arg version "${active_ver}" \
      --arg release_mode "${active_mode}" \
      --arg commit "${active_sha}" \
      --arg commit_url "${SERVER_URL}/${REPO}/commit/${active_sha}" \
      --arg duration "${el_str}" \
      --arg trigger "${active_event}" \
      --arg actor "${active_actor}" \
      --arg url "${active_url}" \
      --arg beta_gcs_uri "gs://cfbosh-cicd/cpi_releases/bosh_gdch_cpi_release_${active_ver#v}.tgz" \
      --arg prod_gcs_uri "N/A (snapshot mode — internal gs://cfbosh-cicd only)" \
      --arg s_build "${s_build}" \
      --arg s_concourse "${s_concourse}" \
      --arg s_vuln "${s_vuln}" \
      --arg s_release "${s_release}" \
      '{
        id: $id,
        run_number: "#1",
        timestamp: $timestamp,
        status: "running",
        version: $version,
        release_mode: $release_mode,
        skip_concourse: false,
        sha256: "",
        commit: $commit,
        commit_url: $commit_url,
        duration: $duration,
        duration_s: null,
        trigger: $trigger,
        actor: $actor,
        url: $url,
        release_url: "",
        concourse_log: "gs://cfbosh-cicd/cicd-deployment-logs/",
        beta_gcs_uri: $beta_gcs_uri,
        prod_gcs_uri: $prod_gcs_uri,
        stages: {
          build_candidate: $s_build,
          concourse: $s_concourse,
          vuln_scan: $s_vuln,
          release: $s_release
        }
      }')"

    jq --argjson entry "${active_entry}" \
      '([$entry] + [.[] | select((.id | tostring) != ($entry.id | tostring))])' \
      "${RUNS_JSON}" >"${RUNS_JSON}.tmp" && mv "${RUNS_JSON}.tmp" "${RUNS_JSON}"
  done < <(jq -c '.workflow_runs[]? | select(.status != "completed")' "${RUNS_JSON}.gh" 2>/dev/null || true)

  rm -f "${RUNS_JSON}.gh"
fi

renumber_runs() {
  # Assign sequential #1..#N in chronological (run id) order, newest first in the file
  jq '.[0:100]
    | sort_by((.id | tonumber?) // 0)
    | to_entries
    | map(.value + {run_number: ("#" + ((.key + 1) | tostring))})
    | sort_by(-((.run_number | ltrimstr("#") | tonumber?) // 0))' \
    "${RUNS_JSON}" >"${RUNS_JSON}.tmp"
  mv "${RUNS_JSON}.tmp" "${RUNS_JSON}"
}

# RECONCILE_ONLY=true (used by the deploy-dashboard workflow on pushes): do not record a new
# run, only merge live + committed data and renumber, so a push never overwrites Pages with
# stale committed data.
if [[ "${RECONCILE_ONLY:-false}" == "true" ]]; then
  renumber_runs
  echo "[INFO] Reconciled ${RUNS_JSON}: $(jq 'length' "${RUNS_JSON}") run(s), no new run recorded (RECONCILE_ONLY=true)."
  exit 0
fi

NEW_ENTRY="$(jq -n \
  --arg id "${RUN_ID}" \
  --arg run_number "#${RUN_NUMBER}" \
  --arg timestamp "${TIMESTAMP}" \
  --arg status "${OVERALL_STATUS}" \
  --arg version "${RELEASE_TAG}" \
  --arg release_mode "${RELEASE_MODE_VAL}" \
  --arg skip_concourse "${SKIP_CONCOURSE:-false}" \
  --arg sha256 "${RELEASE_SHA256:-}" \
  --arg commit "${COMMIT_SHA}" \
  --arg commit_url "${COMMIT_URL}" \
  --arg duration "${DURATION_STR}" \
  --argjson duration_s "${DURATION_SEC}" \
  --arg trigger "${TRIGGER}" \
  --arg actor "${ACTOR}" \
  --arg url "${RUN_URL}" \
  --arg release_url "${RELEASE_URL}" \
  --arg concourse_log "${CONCOURSE_LOG_URI:-gs://cfbosh-cicd/cicd-deployment-logs/}" \
  --arg beta_gcs_uri "gs://cfbosh-cicd/cpi_releases/bosh_gdch_cpi_release_${RELEASE_VERSION#v}.tgz" \
  --arg prod_gcs_uri "${PROD_GCS_URI}" \
  --arg s_build "${STAGE_BUILD}" \
  --arg s_concourse "${STAGE_CONCOURSE}" \
  --arg s_vuln "${STAGE_VULN}" \
  --arg s_release "${STAGE_RELEASE}" \
  '{
    id: $id,
    run_number: $run_number,
    timestamp: $timestamp,
    status: $status,
    version: $version,
    release_mode: $release_mode,
    skip_concourse: ($skip_concourse == "true"),
    sha256: $sha256,
    commit: $commit,
    commit_url: $commit_url,
    duration: $duration,
    duration_s: $duration_s,
    trigger: $trigger,
    actor: $actor,
    url: $url,
    release_url: $release_url,
    concourse_log: $concourse_log,
    beta_gcs_uri: $beta_gcs_uri,
    prod_gcs_uri: $prod_gcs_uri,
    stages: {
      build_candidate: $s_build,
      concourse: $s_concourse,
      vuln_scan: $s_vuln,
      release: $s_release
    }
  }')"

jq --argjson entry "${NEW_ENTRY}" \
  '([$entry] + [.[] | select((.id | tostring) != ($entry.id | tostring))])' \
  "${RUNS_JSON}" >"${RUNS_JSON}.tmp"
mv "${RUNS_JSON}.tmp" "${RUNS_JSON}"
renumber_runs

ASSIGNED_RUN_NUM="$(jq -r --arg id "${RUN_ID}" '.[] | select((.id | tostring) == $id) | .run_number // "#1"' "${RUNS_JSON}" | head -n 1)"
echo "[INFO] Updated ${RUNS_JSON} with run ${ASSIGNED_RUN_NUM} (id=${RUN_ID}, status=${OVERALL_STATUS})"

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "## 🚀 CFBOSH GDC CPI Release Pipeline Summary"
    echo ""
    if [[ "${OVERALL_STATUS}" == "success" ]]; then
      echo "**Overall Status:** ✅ **SUCCESS**"
    else
      echo "**Overall Status:** ❌ **FAILURE**"
    fi
    echo ""
    echo "| Property | Value |"
    echo "| :--- | :--- |"
    echo "| **Release Version** | [\`${RELEASE_TAG}\`](${RELEASE_URL}) |"
    echo "| **Release Mode** | \`${RELEASE_MODE:-release}\` |"
    echo "| **Commit** | [\`${COMMIT_SHA}\`](${COMMIT_URL}) |"
    echo "| **Skip Concourse** | \`${SKIP_CONCOURSE:-false}\` |"
    echo "| **Duration** | \`${DURATION_STR}\` |"
    echo "| **Candidate GCS URI** | \`gs://cfbosh-cicd/cpi_releases/bosh_gdch_cpi_release_${RELEASE_VERSION#v}.tgz\` |"
    echo "| **Production GCS URI** | \`gs://gdc-external-sap/cpi_releases/bosh-gdc-cpi-${RELEASE_VERSION#v}.tgz\` |"
    echo ""
    echo "### Stage Breakdown"
    echo ""
    echo "| Stage | Status |"
    echo "| :--- | :--- |"
    echo "| 1. Build Candidate CPI & Vulnerability Scan | \`${STAGE_BUILD}\` |"
    echo "| 2. Concourse Deployment Pipeline (GKE) | \`${STAGE_CONCOURSE}\` (skip_concourse=\`${SKIP_CONCOURSE:-false}\`) |"
    echo "| 3. Publish Release (GitHub Release & GCS) | \`${STAGE_RELEASE}\` |"
  } >>"${GITHUB_STEP_SUMMARY}"
fi

TARGET_BRANCH="${DASHBOARD_PUSH_BRANCH:-main}"
if [[ "${PUSH_DASHBOARD_DATA:-true}" == "true" && "${GITHUB_ACTIONS:-false}" == "true" && "${TARGET_BRANCH}" == "main" ]]; then
  echo "[INFO] Committing updated dashboard/data/runs.json to ${TARGET_BRANCH}..."
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
  git add "${RUNS_JSON}"
  if ! git diff --cached --quiet; then
    git commit -q -m "chore(dashboard): record release pipeline run ${ASSIGNED_RUN_NUM} (${RELEASE_TAG}) [skip ci]" \
      -m "Signed-off-by: github-actions[bot] <41898282+github-actions[bot]@users.noreply.github.com>"
    pushed="false"
    for attempt in 1 2 3; do
      if git push origin "HEAD:refs/heads/${TARGET_BRANCH}"; then
        pushed="true"
        break
      fi
      echo "[WARN] Push to ${TARGET_BRANCH} rejected (attempt ${attempt}/3); rebasing onto latest ${TARGET_BRANCH}..."
      git fetch origin "${TARGET_BRANCH}" && git rebase "origin/${TARGET_BRANCH}" || {
        git rebase --abort || true
        break
      }
    done
    if [[ "${pushed}" != "true" ]]; then
      echo "[WARN] Could not push dashboard update to ${TARGET_BRANCH}; the deployed Pages data still contains this run."
    fi
  fi
fi

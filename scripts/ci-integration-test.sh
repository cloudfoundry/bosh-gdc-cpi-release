#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Google LLC
#
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(pwd)"

# shellcheck source=scripts/ci-common.sh
source "${SCRIPT_DIR}/ci-common.sh"

check_ci_preconditions

CPI_CONFIG_FILE="${CPI_CONFIG_FILE:-/tmp/cpi_config_$$.json}"
CPI_INTEG_FILTER="${CPI_INTEG_FILTER:-TestMultizone}"
CPI_INTEG_TIMEOUT="${CPI_INTEG_TIMEOUT:-50m}"

cleanup() {
  local exit_code=$?
  rm -f "${CPI_CONFIG_FILE}"
  rm -f "${REPO_ROOT}"/bosh-cpi/integration/bosh-gdch-vm-access*
  complete_pr_check_run "${exit_code}"
}
trap cleanup EXIT

echo "=================================================================="
echo "Setting up GDC CPI configuration..."
echo "=================================================================="
setup_cpi_config "${CPI_CONFIG_FILE}" "${GDC_CPI_CONFIG:-}"

echo "=================================================================="
echo "Running BOSH CPI integration tests (filter: ${CPI_INTEG_FILTER})..."
echo "=================================================================="
cd "${REPO_ROOT}"
go test -v -timeout="${CPI_INTEG_TIMEOUT}" ./bosh-cpi/integration/... \
  -run "${CPI_INTEG_FILTER}" \
  --configPath="${CPI_CONFIG_FILE}"

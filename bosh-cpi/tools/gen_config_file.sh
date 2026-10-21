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

# shellcheck disable=SC2002,SC2086

set -e

usage() {
  echo
  echo "Usage: $0 -lcf <login_config_file> -org <org> -pkf <service_account_private_key_file> -ssf <storage_secret_file> -mbus <agent_mbus> [-gtb <global_trust_bundle_file>] [-oaef <org_api_endpoint_file>] [-image_project <image_project>] [-disable_ssh_proxy] [-enable_external_access] [-stemcell_path <stemcell_path>] [-use_metadata_server] [-use_disk_hotplug] [-use_cloud_nat_gateways] [-refresh_image_import] [-use_tier1_network]"
  exit 1
}

login_config_file=""
pk_json_data=""
storage_secret=""
org_api_endpoint_json=""
mbus="" # i.e "https://user:password@127.0.0.1:4321/agent"
ntp=[]
image_project=""
disable_ssh_proxy=false
vmea_ports=[]
enable_external_access=false
stemcell_path=""
global_trust_bundle=""
isMultiZone=false
use_metadata_server=false
use_disk_hotplug=false
use_cloud_nat_gateways=false
refresh_image_import=false
use_tier1_network=false

while [[ "$#" -gt 0 ]]; do
  case $1 in
  -lcf)
    login_config_file="$2"
    shift
    ;;
  -org)
    org="$2"
    shift
    ;;
  -oaef)
    org_api_endpoint_json=$(cat $2)
    shift
    ;;
  -gtb)
    global_trust_bundle=$(cat $2)
    shift
    ;;
  -pkf)
    pk_json_data=$(cat $2)
    shift
    ;;
  -ssf)
    storage_secret=$(cat $2)
    shift
    ;;
  -mbus)
    mbus="$2"
    shift
    ;;
  -image_project)
    image_project="$2"
    shift
    ;;
  -stemcell_path)
    stemcell_path="$2"
    shift
    ;;
  -disable_ssh_proxy) disable_ssh_proxy=true ;;
  -enable_external_access) enable_external_access=true ;;
  -use_metadata_server) use_metadata_server=true ;;
  -use_disk_hotplug) use_disk_hotplug=true ;;
  -use_cloud_nat_gateways) use_cloud_nat_gateways=true ;;
  -refresh_image_import) refresh_image_import=true ;;
  -use_tier1_network) use_tier1_network=true ;;
  esac
  shift
done

if [[ -z "$login_config_file" || -z "$org" || -z "$pk_json_data" || -z "$storage_secret" || -z "$mbus" ]]; then
  echo "Error: login_config_file, org, service_account_private_key_file, storage_secret_file and mbus cannot be empty"
  usage
fi

# Read login_config_file to get `org_admin_url` and `ca_data`
org_admin_url=$(cat $login_config_file | yq -o=json | jq '.spec.cluster[] | select(.name | endswith("admin") and (test("global") | not)) | .server')
cadata=$(cat $login_config_file | yq -o=json | jq '.spec.cluster[] | select(.name | endswith("admin")) | .certificateAuthorityData')

if ((${#global_trust_bundle} != 0)); then
  cadata="\"$global_trust_bundle\""
fi

# Read org API endpoint config json file
if ((${#org_api_endpoint_json} == 0)); then
  global_api=\"\"
  zonal_apis={}
else
  isMultiZone=true
  global_api=\"$(echo $org_api_endpoint_json | jq -r .orgAPIEndpoints.globalAPI)\"
  zonal_apis=$(echo $org_api_endpoint_json | jq -r .orgAPIEndpoints.zonalAPIs)
fi

# Read private key file
data=$(echo $pk_json_data | jq .)

if [[ -n "$stemcell_path" ]]; then
  echo "Extracting Stemcell..."
  stemcell_dir=$(dirname "$stemcell_path")
  tar -xf "$stemcell_path" -C $stemcell_dir
  stemcell_image_path="$stemcell_dir/image"
  stemcell_manifest_path="$stemcell_dir/stemcell.MF"
fi

pk_type=$(echo $data | jq .type)
sa_type="\"gdch_service_account\""
if [ "$pk_type" != "$sa_type" ]; then
  echo
  echo "Error: invalid service account private key file. Got type $pk_type , expected $sa_type"
  exit 1
fi

name=$(echo $data | jq .name)
project=$(echo $data | jq .project)
private_key_id=$(echo $data | jq .private_key_id)
private_key=$(echo $data | jq .private_key)
token_uri=$(echo $data | jq .token_uri)

if $enable_external_access; then
  vmea_ports="[
      {
        \"name\": \"ssh\",
        \"protocol\": \"TCP\",
        \"port\": 22
      }
    ]"
fi

# Read storage secret to get `access-key-id` and `secret-access-key`
secret_data=$(echo $storage_secret | jq '.data')
access_key_id=$(echo $secret_data | jq -r '.["access-key-id"]')
access_key=$(echo $secret_data | jq -r '.["secret-access-key"]')

echo "### Parameters:"
echo "Org: $org"
echo "Org admin URL: $org_admin_url"
echo "CA Cert: $cadata"
echo "PK Json: $pk_json_data"
echo "Storage secret data: $secret_data"
echo "Storage access key id: $access_key_id"
echo "Storage access key: $access_key"
echo "Agent.mbus: $mbus"
echo "Agent.ntp: $ntp"

echo "Image project: $image_project"
echo "Stemcell path: $stemcell_path"
echo "Disable SSH Proxy: $disable_ssh_proxy"
echo "Enable VM external access: $enable_external_access"
echo "Use metadata server: $use_metadata_server"
echo "Use disk hotplug: $use_disk_hotplug"
echo "Refresh Image Import: $refresh_image_import"

# Create config JSON
json_data=$(
  cat <<EOF
{
  "gdch": {
    "org": "$org",
    "orgAdminURL": $org_admin_url,
    "orgAPIEndpoints": {
      "globalAPI": $global_api,
      "zonalAPIs": $zonal_apis
    },
    "serviceAccountPKConfig": {
      "serviceAccountName": $name,
      "project": $project,
      "tokenUri": $token_uri,
      "privateKeyID": $private_key_id,
      "privateKey": $private_key
    },
    "caData": $cadata,
    "storageCredential": {
      "accessKeyID": "$access_key_id",
      "accessKey": "$access_key"
    },
    "externalAccessPorts": $vmea_ports,
    "imageProject": "$image_project",
    "disableSSHProxy": $disable_ssh_proxy,
    "stemcellInfo": {
      "imagePath": "$stemcell_image_path",
      "manifestPath": "$stemcell_manifest_path"
    },
    "isMultiZone":$isMultiZone,
    "useMetadataServer": $use_metadata_server,
    "useDiskHotplug": $use_disk_hotplug,
    "useCloudNatGateways": $use_cloud_nat_gateways,
    "refreshImageImport": $refresh_image_import,
    "useTier1Network": $use_tier1_network
  },
  "agent": {
    "mbus": "$mbus",
    "ntp": $ntp
  }
}
EOF
)

output_json_file="config.json"

echo "$json_data" >"$output_json_file"

# Create config YML
cpi_property_yml=$(
  cat <<EOF
gdch:
   org: ${org}
   orgAdminURL: ${org_admin_url//\"/}
   orgAPIEndpoints:
      globalAPI: ${global_api//\"/}
      zonalAPIs:
$(echo "$zonal_apis" | jq -c '.' | yq -p json -o yaml | sed "s/^/$(printf '%9s' '')/")
   serviceAccountPKConfig:
      serviceAccountName: ${name//\"/}
      project: ${project//\"/}
      tokenUri: ${token_uri//\"/}
      privateKeyID: ${private_key_id//\"/}
      privateKey: ${private_key//\"/}
   caData: ${cadata//\"/}
   storageCredential:
      accessKeyID: ${access_key_id}
      accessKey: ${access_key}
   externalAccessPorts: ${vmea_ports}
   imageProject: ${image_project}
   disableSSHProxy: ${disable_ssh_proxy}
   stemcellInfo:
    imagePath: ${stemcell_image_path}
    manifestPath: ${stemcell_manifest_path}
   isMultiZone: ${isMultiZone}
   useMetadataServer: ${use_metadata_server}
   useDiskHotplug: ${use_disk_hotplug}
   useCloudNatGateways: ${use_cloud_nat_gateways}
   refreshImageImport: ${refresh_image_import}
   useTier1Network: ${use_tier1_network}
agent:
    mbus: ${mbus}
    ntp: []
EOF
)

output_yml_file="config.yml"

echo "$cpi_property_yml" >"$output_yml_file"

echo "JSON file '$output_json_file' created with base64-encoded kubeconfig."
echo "YML file '$output_yml_file' created with base64-encoded kubeconfig."
echo "*** NTP is unset. Please update JSON file for NTP if needed ***"

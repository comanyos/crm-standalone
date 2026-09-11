#!/usr/bin/env bash
set -euo pipefail

: "${AZURE_STORAGE_ACCOUNT_NAME:?AZURE_STORAGE_ACCOUNT_NAME is required}"
: "${AZURE_STORAGE_CONTAINER:?AZURE_STORAGE_CONTAINER is required}"

install_dir=/opt/savvy-crm
backup_dir=/var/lib/savvy-crm/backups
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_file="$backup_dir/crm-$timestamp.dump"
checksum_file="$backup_file.sha256"

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"

cleanup() {
  rm -f "$backup_file" "$checksum_file"
}
trap cleanup EXIT

cd "$install_dir"
docker compose --env-file runtime.env -f compose.yaml exec -T postgres \
  pg_dump --username postgres --dbname crm --format=custom --compress=6 >"$backup_file"
sha256sum "$backup_file" >"$checksum_file"

token=$(curl --fail --silent --show-error \
  --header Metadata:true \
  "http://169.254.169.254/metadata/identity/oauth2/token?api-version=2019-08-01&resource=https%3A%2F%2Fstorage.azure.com%2F" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')

upload() {
  local source_file=$1
  local blob_name=$2
  local request_date
  request_date=$(LC_ALL=C date -u '+%a, %d %b %Y %H:%M:%S GMT')
  curl --fail --silent --show-error \
    --request PUT \
    --upload-file "$source_file" \
    --header "Authorization: Bearer $token" \
    --header "x-ms-date: $request_date" \
    --header "x-ms-version: 2023-11-03" \
    --header "x-ms-blob-type: BlockBlob" \
    "https://${AZURE_STORAGE_ACCOUNT_NAME}.blob.core.windows.net/${AZURE_STORAGE_CONTAINER}/${blob_name}"
}

upload "$backup_file" "$(basename "$backup_file")"
upload "$checksum_file" "$(basename "$checksum_file")"

trap - EXIT
find "$backup_dir" -type f -mtime +7 -delete

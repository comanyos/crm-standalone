#!/usr/bin/env bash
set -euo pipefail
umask 077

key_vault_name=savvy-crm-kv-8bc52d
storage_account_name=stsavvycrm8bc52d
deployment_container=deployment
deployment_blob=savvy-crm-beta-20260912-011.tar.gz
acr_login_server=savvycrm8bc52d.azurecr.io
image_tag=beta-20260912-011
install_dir=/opt/savvy-crm
state_dir=/var/lib/savvy-crm
data_device=/dev/disk/azure/scsi1/lun0

metadata_token() {
  local resource=$1
  curl --fail --silent --show-error \
    --retry 12 --retry-delay 5 --retry-all-errors \
    --header Metadata:true \
    "http://169.254.169.254/metadata/identity/oauth2/token?api-version=2019-08-01&resource=${resource}" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])'
}

vault_secret() {
  local name=$1
  curl --fail --silent --show-error \
    --retry 12 --retry-delay 5 --retry-all-errors \
    --header "Authorization: Bearer ${vault_token}" \
    "https://${key_vault_name}.vault.azure.net/secrets/${name}?api-version=7.4" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin)["value"], end="")'
}

# Optional provider credentials return empty until the corresponding external
# app has been approved. A partial set is intentionally left partial so CRM
# config validation fails loudly instead of exposing a broken Connect button.
vault_secret_optional() {
  local name=$1
  local response
  local value
  if ! response=$(curl --fail --silent --show-error \
    --retry 3 --retry-delay 2 --retry-all-errors \
    --header "Authorization: Bearer ${vault_token}" \
    "https://${key_vault_name}.vault.azure.net/secrets/${name}?api-version=7.4" \
    2>/dev/null); then
    printf ''
    return
  fi
  value=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["value"], end="")' 2>/dev/null) || value=
  printf '%s' "$value"
}

cloud-init status --wait >/dev/null 2>&1 || true
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq ca-certificates curl docker.io docker-compose-v2 e2fsprogs openssl python3

if [ ! -b "$data_device" ]; then
  echo "Expected Azure data disk is missing: $data_device" >&2
  exit 1
fi

systemctl stop docker.service docker.socket || true
if ! blkid "$data_device" >/dev/null 2>&1; then
  mkfs.ext4 -q -L savvycrmdata "$data_device"
fi
data_uuid=$(blkid -s UUID -o value "$data_device")
mkdir -p "$state_dir"
if ! grep -q "UUID=${data_uuid}" /etc/fstab; then
  printf 'UUID=%s %s ext4 defaults,nofail 0 2\n' "$data_uuid" "$state_dir" >>/etc/fstab
fi
mountpoint -q "$state_dir" || mount "$state_dir"
mkdir -p "$state_dir/docker" "$state_dir/backups"
chmod 700 "$state_dir/backups"

install -d -m 0755 /etc/docker
printf '{\n  "data-root": "%s/docker",\n  "live-restore": true,\n  "log-driver": "json-file",\n  "log-opts": {"max-size": "10m", "max-file": "3"}\n}\n' "$state_dir" >/etc/docker/daemon.json
systemctl daemon-reload
systemctl enable --now docker

install -d -m 0755 "$install_dir"
bundle=$(mktemp /tmp/savvy-crm-config.XXXXXX.tar.gz)
storage_token=$(metadata_token 'https%3A%2F%2Fstorage.azure.com%2F')
curl --fail --silent --show-error \
  --retry 12 --retry-delay 5 --retry-all-errors \
  --header "Authorization: Bearer ${storage_token}" \
  --header "x-ms-version: 2023-11-03" \
  --output "$bundle" \
  "https://${storage_account_name}.blob.core.windows.net/${deployment_container}/${deployment_blob}"
tar -xzf "$bundle" -C "$install_dir"

install -d -m 0700 /etc/savvy-crm/pki
install -d -m 0755 "$install_dir/tls/postgres"
if [ ! -s /etc/savvy-crm/pki/ca.key ]; then
  openssl req -x509 -newkey rsa:4096 -sha256 -nodes -days 3650 \
    -subj '/CN=Savvy CRM internal database CA' \
    -keyout /etc/savvy-crm/pki/ca.key \
    -out "$install_dir/tls/postgres/ca.crt" >/dev/null 2>&1
  openssl req -newkey rsa:3072 -sha256 -nodes \
    -subj '/CN=postgres' \
    -keyout "$install_dir/tls/postgres/server.key" \
    -out /etc/savvy-crm/pki/postgres.csr >/dev/null 2>&1
  printf 'subjectAltName=DNS:postgres\nextendedKeyUsage=serverAuth\n' >/etc/savvy-crm/pki/postgres.ext
  openssl x509 -req -sha256 -days 825 \
    -in /etc/savvy-crm/pki/postgres.csr \
    -CA "$install_dir/tls/postgres/ca.crt" \
    -CAkey /etc/savvy-crm/pki/ca.key \
    -CAcreateserial \
    -extfile /etc/savvy-crm/pki/postgres.ext \
    -out "$install_dir/tls/postgres/server.crt" >/dev/null 2>&1
fi
chown 70:70 "$install_dir/tls/postgres/server.key" "$install_dir/tls/postgres/server.crt"
chmod 0600 "$install_dir/tls/postgres/server.key"
chmod 0644 "$install_dir/tls/postgres/server.crt" "$install_dir/tls/postgres/ca.crt"

vault_token=$(metadata_token 'https%3A%2F%2Fvault.azure.net')
crm_db_admin_password=$(vault_secret postgres-admin-password)
crm_db_app_password=$(vault_secret crm-db-app-password)
crm_db_drain_password=$(vault_secret crm-db-drain-password)
crm_metrics_token=$(vault_secret crm-metrics-token)
otp_pepper=$(vault_secret otp-pepper)
smtp_password=$(vault_secret smtp-password)
auth_google_client_id=$(vault_secret auth-google-client-id)
auth_google_client_secret=$(vault_secret auth-google-client-secret)
google_oauth_client_id=$(vault_secret_optional google-oauth-client-id)
google_oauth_client_secret=$(vault_secret_optional google-oauth-client-secret)
google_oauth_token_key=$(vault_secret_optional google-oauth-token-key)
google_oauth_redirect_url=
if [ -n "$google_oauth_client_id" ]; then
  google_oauth_redirect_url=https://crmapi.savvyadvi.com/integrations/google/callback
fi
lead_capture_secret_key=$(vault_secret lead-capture-secret-key)
workflow_webhook_secret_key=$(vault_secret workflow-webhook-secret-key)
meta_app_id=$(vault_secret_optional meta-app-id)
meta_app_secret=$(vault_secret_optional meta-app-secret)
meta_webhook_verify_token=$(vault_secret_optional meta-webhook-verify-token)
meta_token_key=$(vault_secret_optional meta-token-key)
meta_oauth_redirect_url=
if [ -n "$meta_app_id" ]; then
  meta_oauth_redirect_url=https://crmapi.savvyadvi.com/integrations/meta/callback
fi
acr_username=$(vault_secret acr-pull-username)
acr_password=$(vault_secret acr-pull-password)

runtime_env=$(mktemp /tmp/savvy-crm-runtime.XXXXXX)
{
  printf 'ACR_LOGIN_SERVER=%s\n' "$acr_login_server"
  printf 'IMAGE_TAG=%s\n' "$image_tag"
  printf 'CRM_DB_ADMIN_PASSWORD=%s\n' "$crm_db_admin_password"
  printf 'CRM_DB_APP_PASSWORD=%s\n' "$crm_db_app_password"
  printf 'CRM_DB_DRAIN_PASSWORD=%s\n' "$crm_db_drain_password"
  printf 'CRM_METRICS_TOKEN=%s\n' "$crm_metrics_token"
  printf 'OTP_PEPPER=%s\n' "$otp_pepper"
  printf 'SMTP_PASSWORD=%s\n' "$smtp_password"
  printf 'AUTH_GOOGLE_CLIENT_ID=%s\n' "$auth_google_client_id"
  printf 'AUTH_GOOGLE_CLIENT_SECRET=%s\n' "$auth_google_client_secret"
  printf 'GOOGLE_OAUTH_CLIENT_ID=%s\n' "$google_oauth_client_id"
  printf 'GOOGLE_OAUTH_CLIENT_SECRET=%s\n' "$google_oauth_client_secret"
  printf 'GOOGLE_OAUTH_REDIRECT_URL=%s\n' "$google_oauth_redirect_url"
  printf 'GOOGLE_OAUTH_TOKEN_KEY=%s\n' "$google_oauth_token_key"
  printf 'LEAD_CAPTURE_SECRET_KEY=%s\n' "$lead_capture_secret_key"
  printf 'WORKFLOW_WEBHOOK_SECRET_KEY=%s\n' "$workflow_webhook_secret_key"
  printf 'META_APP_ID=%s\n' "$meta_app_id"
  printf 'META_APP_SECRET=%s\n' "$meta_app_secret"
  printf 'META_OAUTH_REDIRECT_URL=%s\n' "$meta_oauth_redirect_url"
  printf 'META_WEBHOOK_VERIFY_TOKEN=%s\n' "$meta_webhook_verify_token"
  printf 'META_TOKEN_KEY=%s\n' "$meta_token_key"
} >"$runtime_env"
install -m 0600 "$runtime_env" "$install_dir/runtime.env"

printf '%s' "$acr_password" | docker login "$acr_login_server" --username "$acr_username" --password-stdin >/dev/null

chmod 0750 "$install_dir/backup.sh"
install -d -m 0755 /etc/savvy-crm
{
  printf 'AZURE_STORAGE_ACCOUNT_NAME=%s\n' "$storage_account_name"
  printf 'AZURE_STORAGE_CONTAINER=db-backups\n'
} >/etc/savvy-crm/backup.env
chmod 0644 /etc/savvy-crm/backup.env

install -m 0644 "$install_dir/savvy-crm.service" /etc/systemd/system/savvy-crm.service
install -m 0644 "$install_dir/savvy-crm-backup.service" /etc/systemd/system/savvy-crm-backup.service
install -m 0644 "$install_dir/savvy-crm-backup.timer" /etc/systemd/system/savvy-crm-backup.timer
systemctl daemon-reload
systemctl enable savvy-crm.service
systemctl restart savvy-crm.service
systemctl enable --now savvy-crm-backup.timer

cd "$install_dir"
docker compose --env-file runtime.env -f compose.yaml ps
docker compose --env-file runtime.env -f compose.yaml exec -T crm-api \
  wget -q -O /dev/null http://127.0.0.1:9431/readyz
rm -f "$runtime_env" "$bundle"
echo 'Savvy CRM VM bootstrap completed'

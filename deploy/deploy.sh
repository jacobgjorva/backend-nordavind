#!/usr/bin/env bash
# Bygg og deploy fra utviklingsmaskinen:
#   SERVER=root@1.2.3.4 DOMAIN=app.dittdomene.no bash deploy/deploy.sh
set -euo pipefail

: "${SERVER:?Sett SERVER=bruker@ip}"
: "${DOMAIN:?Sett DOMAIN=app.dittdomene.no}"

BACKEND_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FRONTEND_DIR="$BACKEND_DIR/../nordavind"

echo "== Bygger backend (linux/amd64, ren Go — ingen CGO) =="
cd "$BACKEND_DIR"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /tmp/nordavind-server ./cmd/server

echo "== Bygger frontend mot https://$DOMAIN =="
cd "$FRONTEND_DIR"
VITE_API_BASE_URL="https://$DOMAIN/v1" npm run build

echo "== Kopierer til serveren =="
rsync -az /tmp/nordavind-server "$SERVER":/opt/nordavind/server.new
rsync -az --delete "$FRONTEND_DIR/dist/" "$SERVER":/opt/nordavind/frontend/

echo "== Bytter binær og restarter =="
ssh "$SERVER" '
  mv /opt/nordavind/server.new /opt/nordavind/server &&
  chown nordavind:nordavind /opt/nordavind/server &&
  chmod 755 /opt/nordavind/server &&
  systemctl restart nordavind caddy &&
  sleep 2 &&
  systemctl is-active nordavind caddy
'

echo "== Helsesjekk =="
curl -s -o /dev/null -w "https://$DOMAIN → %{http_code}\n" "https://$DOMAIN"
echo "Deploy ferdig."

#!/usr/bin/env bash
# Engangs-oppsett av en fersk Hetzner-boks (Ubuntu 24.04). Kjøres som root:
#   NORDAVIND_DOMAIN=app.dittdomene.no bash setup.sh
set -euo pipefail

: "${NORDAVIND_DOMAIN:?Sett NORDAVIND_DOMAIN=app.dittdomene.no}"

echo "== Pakker =="
apt-get update -qq
apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl sqlite3 ufw

echo "== Caddy =="
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
apt-get update -qq && apt-get install -y -qq caddy

echo "== Bruker og kataloger =="
id nordavind &>/dev/null || useradd --system --home /opt/nordavind --shell /usr/sbin/nologin nordavind
mkdir -p /opt/nordavind/data /opt/nordavind/frontend /opt/nordavind/backup
chown -R nordavind:nordavind /opt/nordavind

echo "== Miljøfil (fyll inn hemmelighetene!) =="
if [ ! -f /opt/nordavind/env ]; then
  cat > /opt/nordavind/env <<EOF
PORT=8080
PUBLIC_BASE_URL=https://${NORDAVIND_DOMAIN}
ALLOWED_ORIGIN=https://${NORDAVIND_DOMAIN}
DB_PATH=/opt/nordavind/data/nordavind.db
UPSTREAM_API_KEY=FYLL-INN
# MAIL_* og evt. andre hemmeligheter legges til her ved behov.
EOF
  chown root:nordavind /opt/nordavind/env
  chmod 640 /opt/nordavind/env
fi

echo "== systemd + Caddy-konfig =="
cp "$(dirname "$0")/nordavind.service" /etc/systemd/system/nordavind.service
mkdir -p /etc/caddy
NORDAVIND_DOMAIN="$NORDAVIND_DOMAIN" envsubst < "$(dirname "$0")/Caddyfile" > /etc/caddy/Caddyfile
systemctl daemon-reload
systemctl enable nordavind caddy

echo "== Brannmur: kun ssh + http(s) =="
ufw allow OpenSSH
ufw allow 80/tcp
ufw allow 443/tcp
ufw --force enable

echo "== Daglig SQLite-backup (7 dagers rullering) =="
cat > /etc/cron.daily/nordavind-backup <<'EOF'
#!/bin/sh
d=/opt/nordavind/backup/nordavind-$(date +%u).db
sqlite3 /opt/nordavind/data/nordavind.db ".backup $d" && chown nordavind:nordavind "$d"
EOF
chmod +x /etc/cron.daily/nordavind-backup

echo
echo "Ferdig. Neste steg:"
echo "  1) Rediger /opt/nordavind/env (UPSTREAM_API_KEY m.m.)"
echo "  2) Kjør deploy.sh fra utviklingsmaskinen"

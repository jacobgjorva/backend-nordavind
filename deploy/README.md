# Deploy til Hetzner

Én VPS kjører alt: Go-backend (systemd), SQLite på disk, frontend som statiske
filer bak Caddy med automatisk HTTPS.

## Engangsoppsett

1. Opprett server på hetzner.com → Cloud → CX22 (Ubuntu 24.04, Falkenstein/
   Helsinki). Legg inn SSH-nøkkelen din.
2. Pek DNS: A-record for `app.dittdomene.no` → serverens IP.
3. Kopiér deploy-mappa og kjør oppsettet:

       scp -r deploy root@<ip>:/root/
       ssh root@<ip> 'NORDAVIND_DOMAIN=app.dittdomene.no bash /root/deploy/setup.sh'

4. Fyll inn hemmelighetene på serveren: `ssh root@<ip> nano /opt/nordavind/env`
   (minst `UPSTREAM_API_KEY`).

## Hver deploy

    SERVER=root@<ip> DOMAIN=app.dittdomene.no bash deploy/deploy.sh

Bygger backend (ren Go, kryss-kompilert) og frontend, rsync-er opp, restarter
tjenestene og helsesjekker.

## Drift

- Logger: `journalctl -u nordavind -f`
- Backup: daglig SQLite-kopi i `/opt/nordavind/backup/` (7 dagers rullering);
  ta i tillegg ukentlig Hetzner-snapshot.
- Skalering: større VPS ved behov (resize i panelet). Horisontal skalering
  krever Postgres-migrering — bevisst utsatt.

## SearXNG (websøk)

Backenden søker via en self-hostet SearXNG-instans (Docker, loopback-only på
127.0.0.1:8888) — se `deploy/searxng/`. `setup.sh` installerer og starter den;
`SEARXNG_URL` i `/opt/nordavind/env` peker backenden dit. Tom `SEARXNG_URL`
gir DuckDuckGo-fallback (gjelder også automatisk om instansen dør).
Blokkeringsovervåking: `journalctl -u nordavind | grep "søkemotorer nede"`.

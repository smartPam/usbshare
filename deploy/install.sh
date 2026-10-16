#!/usr/bin/env bash
# Instalator serwera USBShare dla Linuksa (Debian/Ubuntu/Raspberry Pi OS i podobne).
# Użycie:  curl -fsSL https://raw.githubusercontent.com/smartPam/usbshare/main/deploy/install.sh | sudo bash
#
# Co robi:
#   1. instaluje Dockera (jeśli go nie ma),
#   2. ładuje moduły jądra usbip-core i usbip-host (także po restarcie),
#   3. tworzy /opt/usbshare z docker-compose.yml i .env (losowe hasło),
#   4. uruchamia kontener (startuje sam po restarcie komputera),
#   5. dodaje codzienną automatyczną aktualizację (timer systemd).
set -euo pipefail

REPO="${USBSHARE_REPO:-smartPam/usbshare}"
BRANCH="${USBSHARE_BRANCH:-main}"
DIR="${USBSHARE_DIR:-/opt/usbshare}"
RAW="https://raw.githubusercontent.com/${REPO}/${BRANCH}/deploy"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mBŁĄD:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "uruchom jako root (sudo)."

# 1. Docker
if ! command -v docker >/dev/null 2>&1; then
  say "Instaluję Dockera…"
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker >/dev/null 2>&1 || true
docker compose version >/dev/null 2>&1 || die "brak 'docker compose' (plugin). Zainstaluj pakiet docker-compose-plugin."

# 2. Moduły jądra
say "Ładuję moduły jądra usbip…"
if ! modprobe usbip-host 2>/dev/null; then
  if command -v apt-get >/dev/null 2>&1; then
    say "Brak modułu – instaluję linux-modules-extra / usbip…"
    apt-get update -qq || true
    apt-get install -y -qq "linux-modules-extra-$(uname -r)" >/dev/null 2>&1 || true
    apt-get install -y -qq usbip >/dev/null 2>&1 || apt-get install -y -qq linux-tools-generic >/dev/null 2>&1 || true
  fi
  modprobe usbip-host || die "jądro nie ma modułu usbip-host (CONFIG_USBIP_HOST). Zainstaluj moduły dla $(uname -r)."
fi
printf 'usbip-core\nusbip-host\n' > /etc/modules-load.d/usbshare.conf

# 3. Pliki
mkdir -p "$DIR/data"
cd "$DIR"
say "Pobieram docker-compose.yml…"
curl -fsSL "$RAW/docker-compose.yml" -o docker-compose.yml
if [ ! -f .env ]; then
  PW="$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 16 || true)"
  cat > .env <<EOF
# Hasło, które wpisujesz w kliencie USBShare
USBSHARE_PASSWORD=${PW}
# Nazwa serwera widoczna w kliencie
USBSHARE_NAME=$(hostname)
# Puste = udostępnij wszystkie urządzenia. Przykład: USBSHARE_DENY=046d:c52b
USBSHARE_ALLOW=
USBSHARE_DENY=
EOF
  chmod 600 .env
fi

# 4. Start
say "Pobieram i uruchamiam kontener…"
docker compose pull
docker compose up -d

# 5. Automatyczne aktualizacje (codziennie ~04:00)
cat > /etc/systemd/system/usbshare-update.service <<EOF
[Unit]
Description=Aktualizacja serwera USBShare
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
WorkingDirectory=${DIR}
ExecStart=/usr/bin/env bash -c 'curl -fsSL ${RAW}/docker-compose.yml -o docker-compose.yml.new && mv docker-compose.yml.new docker-compose.yml; docker compose pull -q && docker compose up -d && docker image prune -f'
EOF
cat > /etc/systemd/system/usbshare-update.timer <<'EOF'
[Unit]
Description=Codzienna aktualizacja serwera USBShare

[Timer]
OnCalendar=*-*-* 04:00:00
RandomizedDelaySec=30m
Persistent=true

[Install]
WantedBy=timers.target
EOF
systemctl daemon-reload
systemctl enable --now usbshare-update.timer >/dev/null

sleep 3
IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
FP="$(docker logs usbshare-server 2>&1 | grep -o 'Odcisk certyfikatu.*' | tail -1 | sed 's/.*: //')"
PW="$(grep '^USBSHARE_PASSWORD=' .env | cut -d= -f2-)"
echo
say "Gotowe. Serwer USBShare działa."
echo "   Adres w kliencie:  ${IP:-<IP tego komputera>}   port: 7600"
echo "   Hasło:             ${PW}"
echo "   Odcisk certyfikatu: ${FP:-zobacz: docker logs usbshare-server}"
echo "   Konfiguracja:      ${DIR}/.env   (po zmianie: cd ${DIR} && docker compose up -d)"
echo "   Logi:              docker logs -f usbshare-server"

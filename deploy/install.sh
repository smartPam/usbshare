#!/usr/bin/env bash
# Instalator serwera USBShare dla Linuksa: Debian/Ubuntu/Raspberry Pi OS (systemd)
# oraz Alpine (OpenRC), także w kontenerze LXC na Proxmox.
# Użycie:  curl -fsSL https://raw.githubusercontent.com/smartPam/usbshare/main/deploy/install.sh | sudo bash
#
# Co robi:
#   1. sprawdza/ładuje moduły jądra usbip-core i usbip-host (w LXC: moduł ładuje host),
#   2. instaluje Dockera (jeśli go nie ma),
#   3. tworzy /opt/usbshare z docker-compose.yml i .env (losowe hasło),
#   4. uruchamia kontener (startuje sam po restarcie),
#   5. dodaje codzienną automatyczną aktualizację (timer systemd albo cron).
set -euo pipefail

REPO="${USBSHARE_REPO:-smartPam/usbshare}"
BRANCH="${USBSHARE_BRANCH:-main}"
DIR="${USBSHARE_DIR:-/opt/usbshare}"
RAW="https://raw.githubusercontent.com/${REPO}/${BRANCH}/deploy"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mBŁĄD:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "uruchom jako root (sudo)."

HAS_SYSTEMD=0; [ -d /run/systemd/system ] && HAS_SYSTEMD=1
IS_ALPINE=0;   command -v apk >/dev/null 2>&1 && IS_ALPINE=1
IN_LXC=0
if grep -qa 'container=lxc' /proc/1/environ 2>/dev/null || [ "$(systemd-detect-virt -c 2>/dev/null || true)" = "lxc" ]; then
  IN_LXC=1
fi

# 1. Moduły jądra
lxc_help() {
  cat >&2 <<'EOT'

Ten system to kontener LXC. Kontener używa jądra hosta Proxmox, więc:

 1) Na HOŚCIE Proxmox (Shell węzła, nie kontenera) wykonaj:
      modprobe usbip-host
      printf 'usbip-core\nusbip-host\n' > /etc/modules-load.d/usbip.conf

 2) Kontener musi być UPRZYWILEJOWANY (Unprivileged container = No).
    Wyłącz go i dopisz na hoście do /etc/pve/lxc/<ID>.conf:
      features: nesting=1
      lxc.apparmor.profile: unconfined
      lxc.cgroup2.devices.allow: a
      lxc.cap.drop:
      lxc.mount.auto: proc:rw sys:rw cgroup:rw

 3) Uruchom kontener i ponów instalację.

 Najprościej: uruchom na hoście Proxmox skrypt, który zrobi to wszystko sam:
   bash -c "$(curl -fsSL https://raw.githubusercontent.com/smartPam/usbshare/main/deploy/proxmox-lxc.sh)"
EOT
}
if [ -d /sys/bus/usb/drivers/usbip-host ]; then
  say "Moduł usbip-host jest załadowany."
elif [ "$IN_LXC" = 1 ]; then
  lxc_help
  die "moduł usbip-host nie jest załadowany na hoście."
else
  say "Ładuję moduły jądra usbip…"
  if ! modprobe usbip-host 2>/dev/null; then
    if command -v apt-get >/dev/null 2>&1; then
      say "Brak modułu – instaluję linux-modules-extra / usbip…"
      apt-get update -qq || true
      apt-get install -y -qq "linux-modules-extra-$(uname -r)" >/dev/null 2>&1 || true
      apt-get install -y -qq usbip >/dev/null 2>&1 || apt-get install -y -qq linux-tools-generic >/dev/null 2>&1 || true
    fi
    [ "$IS_ALPINE" = 1 ] && say "Alpine: moduły usbip są w jądrze linux-lts (nie ma ich w linux-virt)."
    modprobe usbip-host || die "jądro nie ma modułu usbip-host (CONFIG_USBIP_HOST). Zainstaluj moduły dla $(uname -r)."
  fi
  mkdir -p /etc/modules-load.d
  printf 'usbip-core\nusbip-host\n' > /etc/modules-load.d/usbshare.conf
fi
if [ ! -w /sys/bus/usb/drivers_probe ]; then
  [ "$IN_LXC" = 1 ] && lxc_help
  die "/sys jest tylko do odczytu – serwer nie może przełączać urządzeń USB."
fi

# 2. Docker
if [ "$IS_ALPINE" = 1 ]; then
  if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
    say "Instaluję Dockera (apk)…"
    apk add --no-cache docker docker-cli-compose >/dev/null
  fi
  rc-update add docker default >/dev/null 2>&1 || true
  service docker start >/dev/null 2>&1 || true
else
  if ! command -v docker >/dev/null 2>&1; then
    say "Instaluję Dockera…"
    curl -fsSL https://get.docker.com | sh
  fi
  [ "$HAS_SYSTEMD" = 1 ] && systemctl enable --now docker >/dev/null 2>&1 || true
fi
for _ in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
docker info >/dev/null 2>&1 || die "Docker nie wystartował (sprawdź: service docker status / journalctl -u docker)."
docker compose version >/dev/null 2>&1 || die "brak 'docker compose'. Zainstaluj docker-compose-plugin (Debian) lub docker-cli-compose (Alpine)."

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

# 5. Automatyczne aktualizacje (codziennie)
UPDATE="cd ${DIR} && curl -fsSL ${RAW}/docker-compose.yml -o docker-compose.yml.new && mv docker-compose.yml.new docker-compose.yml; docker compose pull -q && docker compose up -d && docker image prune -f"
if [ "$HAS_SYSTEMD" = 1 ]; then
  cat > /etc/systemd/system/usbshare-update.service <<EOF
[Unit]
Description=Aktualizacja serwera USBShare
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c '${UPDATE}'
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
  UPD_INFO="timer systemd usbshare-update (start teraz: systemctl start usbshare-update)"
elif [ -d /etc/periodic/daily ]; then
  printf '#!/bin/sh\n%s >/var/log/usbshare-update.log 2>&1\n' "$UPDATE" > /etc/periodic/daily/usbshare-update
  chmod +x /etc/periodic/daily/usbshare-update
  rc-update add crond default >/dev/null 2>&1 || true
  service crond start >/dev/null 2>&1 || true
  UPD_INFO="cron /etc/periodic/daily/usbshare-update (start teraz: /etc/periodic/daily/usbshare-update)"
else
  UPD_INFO="brak (dodaj do crona: ${UPDATE})"
fi

sleep 3
IP="$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1)"
FP="$(docker logs usbshare-server 2>&1 | grep -o 'Odcisk certyfikatu.*' | tail -1 | sed 's/.*: //')"
PW="$(grep '^USBSHARE_PASSWORD=' .env | cut -d= -f2-)"
echo
say "Gotowe. Serwer USBShare działa."
echo "   Adres w kliencie:   ${IP:-<IP tego komputera>}   port: 7600"
echo "   Hasło:              ${PW}"
echo "   Odcisk certyfikatu: ${FP:-zobacz: docker logs usbshare-server}"
echo "   Konfiguracja:       ${DIR}/.env   (po zmianie: cd ${DIR} && docker compose up -d)"
echo "   Aktualizacje:       ${UPD_INFO}"
echo "   Logi:               docker logs -f usbshare-server"

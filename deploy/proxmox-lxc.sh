#!/usr/bin/env bash
# Tworzy na Proxmox VE lekki kontener LXC (Alpine) z serwerem USBShare.
# Uruchom w Shell WĘZŁA Proxmox (nie w kontenerze):
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/smartPam/usbshare/main/deploy/proxmox-lxc.sh)"
#
# Opcje (zmienne środowiskowe przed poleceniem):
#   CTID=120  STORAGE=local-lvm  BRIDGE=vmbr0  IP=192.168.1.50/24  GW=192.168.1.1
#   CT_HOSTNAME=usbshare  MEMORY=256  DISK=2
set -euo pipefail

REPO="${USBSHARE_REPO:-smartPam/usbshare}"
BRANCH="${USBSHARE_BRANCH:-main}"
RAW="https://raw.githubusercontent.com/${REPO}/${BRANCH}/deploy"

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mBŁĄD:\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "uruchom jako root."
command -v pct >/dev/null 2>&1 || die "to nie jest host Proxmox VE (brak pct). Uruchom w Shell węzła."

# 1. Moduły jądra na hoście (kontener korzysta z jądra Proxmoxa)
say "Ładuję moduły usbip w jądrze Proxmoxa ($(uname -r))…"
modprobe usbip-core 2>/dev/null || true
modprobe usbip-host || die "jądro $(uname -r) nie ma modułu usbip-host. Sprawdź: modinfo usbip-host"
printf 'usbip-core\nusbip-host\n' > /etc/modules-load.d/usbip.conf

# 2. Szablon Alpine
TSTORE="${TEMPLATE_STORAGE:-local}"
say "Szukam najnowszego szablonu Alpine…"
pveam update >/dev/null 2>&1 || true
TEMPLATE="$(pveam available --section system | awk '{print $2}' | grep -E '^alpine-3\.[0-9]+-default_[^ ]*_amd64\.tar\.(xz|gz|zst)$' | sort -V | tail -1)"
[ -n "$TEMPLATE" ] || die "nie znaleziono szablonu Alpine (pveam available --section system)."
if ! grep -q "$TEMPLATE" <<<"$(pveam list "$TSTORE")"; then
  say "Pobieram $TEMPLATE do $TSTORE…"
  pveam download "$TSTORE" "$TEMPLATE" >/dev/null
fi

# 3. Kontener
CTID="${CTID:-$(pvesh get /cluster/nextid)}"
if [ -z "${STORAGE:-}" ]; then
  STORES="$(pvesm status 2>/dev/null | awk 'NR>1 {print $1}')"
  for s in local-lvm local-zfs local; do
    if grep -qx "$s" <<<"$STORES"; then STORAGE="$s"; break; fi
  done
fi
BRIDGE="${BRIDGE:-vmbr0}"
NET="name=eth0,bridge=${BRIDGE},ip=${IP:-dhcp}"
[ -n "${GW:-}" ] && NET="${NET},gw=${GW}"

say "Tworzę kontener ${CTID} (Alpine, uprzywilejowany, ${MEMORY:-256} MB RAM, ${DISK:-2} GB dysku na ${STORAGE})…"
pct create "$CTID" "${TSTORE}:vztmpl/${TEMPLATE}" \
  --hostname "${CT_HOSTNAME:-usbshare}" \
  --ostype alpine \
  --cores 1 --memory "${MEMORY:-256}" --swap 256 \
  --rootfs "${STORAGE}:${DISK:-2}" \
  --net0 "$NET" \
  --unprivileged 0 --features nesting=1 \
  --onboot 1 >/dev/null

# Uprawnienia potrzebne do przełączania urządzeń USB (zapis do /sys/bus/usb) i Dockera w LXC
cat >> "/etc/pve/lxc/${CTID}.conf" <<'EOF'
lxc.apparmor.profile: unconfined
lxc.cgroup2.devices.allow: a
lxc.cap.drop:
lxc.mount.auto: proc:rw sys:rw cgroup:rw
EOF

say "Uruchamiam kontener…"
pct start "$CTID"
for _ in $(seq 1 60); do
  pct exec "$CTID" -- sh -c 'ip -4 route | grep -q default' 2>/dev/null && break
  sleep 1
done
pct exec "$CTID" -- sh -c 'ip -4 route | grep -q default' || die "kontener nie dostał adresu IP (sprawdź sieć/DHCP; możesz podać IP=… GW=…)."
sleep 2

# 4. USBShare w kontenerze
say "Instaluję USBShare w kontenerze…"
pct exec "$CTID" -- sh -c 'apk update >/dev/null && apk add --no-cache bash curl >/dev/null'
pct exec "$CTID" -- bash -c "curl -fsSL ${RAW}/install.sh | USBSHARE_REPO=${REPO} USBSHARE_BRANCH=${BRANCH} bash"

echo
say "Kontener ${CTID} gotowy. Startuje automatycznie razem z Proxmoxem."
echo "   Konsola kontenera:  pct enter ${CTID}"
echo "   Uwaga: kontener widzi WSZYSTKIE urządzenia USB hosta. Urządzenia przekazane do maszyn"
echo "   wirtualnych wyklucz w /opt/usbshare/.env (USBSHARE_DENY=vid:pid), potem: docker compose up -d"

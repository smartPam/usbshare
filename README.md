# USBShare

Udostępnianie urządzeń USB przez sieć, podobnie jak VirtualHere. Urządzenie wpinasz do komputera z Linuksem (np. Raspberry Pi albo serwera), a na komputerach z Windows pojawia się ono tak, jakby było podłączone lokalnie.

- **Serwer:** Linux w Dockerze, uruchamia się sam po starcie systemu i sam się aktualizuje.
- **Klient:** Windows (i Linux). Okno w przeglądarce z listą serwerów i urządzeń oraz przyciskami Podłącz i Odłącz.
- **Bezpieczeństwo:** połączenie szyfrowane TLS 1.3 z hasłem i przypinaniem certyfikatu serwera (jak w SSH).
- **Protokół:** otwarty standard USB/IP. Po stronie serwera ruch USB obsługuje moduł jądra Linux `usbip-host`, a po stronie Windows podpisany sterownik [usbip-win2](https://github.com/vadimgrn/usbip-win2).

```
 [urządzenie USB]──Linux: usbip-host ⇄ usbshare-server ══TLS+hasło══ usbshare-client ⇄ usbip-win2 ──[wirtualny port USB w Windows]
```

## 1. Serwer (Linux)

Na komputerze, do którego wpinasz urządzenia, wykonaj:

```bash
curl -fsSL https://raw.githubusercontent.com/smartPam/usbshare/main/deploy/install.sh | sudo bash
```

Instalator:
- instaluje Dockera, jeśli go nie ma,
- ładuje moduły `usbip-core` i `usbip-host` (także po restarcie),
- tworzy katalog `/opt/usbshare` z plikami `docker-compose.yml` i `.env` (losowe hasło),
- uruchamia kontener `usbshare-server` z `restart: unless-stopped`,
- dodaje timer systemd `usbshare-update`, który codziennie pobiera nową wersję obrazu.

Na końcu wypisuje **adres, hasło i odcisk certyfikatu**, które podajesz w kliencie.

Przydatne polecenia:

```bash
docker logs -f usbshare-server                # logi, odcisk certyfikatu
sudo nano /opt/usbshare/.env                  # hasło, nazwa, filtry urządzeń
cd /opt/usbshare && sudo docker compose up -d # zastosuj zmiany
sudo systemctl start usbshare-update          # aktualizacja teraz
```

### Proxmox: kontener LXC

Kontener LXC korzysta z jądra hosta, więc moduł trzeba załadować **na hoście Proxmox** (Shell węzła):

```bash
modprobe usbip-host
printf 'usbip-core\nusbip-host\n' > /etc/modules-load.d/usbip.conf
```

Kontener musi być **uprzywilejowany** (*Unprivileged container: No*), bo serwer zapisuje do `/sys/bus/usb`. Wyłącz go i dopisz do `/etc/pve/lxc/<ID>.conf` na hoście:

```
features: nesting=1
lxc.apparmor.profile: unconfined
lxc.cgroup2.devices.allow: a
lxc.cap.drop:
lxc.mount.auto: proc:rw sys:rw cgroup:rw
```

Potem uruchom kontener i ponów instalację. Kontener widzi wszystkie urządzenia USB hosta, dlatego przez `USBSHARE_DENY` wyklucz urządzenia, których nie chcesz udostępniać, oraz te przekazane do maszyn wirtualnych.

Jeśli nie chcesz uprzywilejowanego LXC, uruchom serwer w maszynie wirtualnej (np. Debian) z przekazanym kontrolerem USB albo urządzeniami USB.

### Ustawienia (`/opt/usbshare/.env`)

| Zmienna | Opis |
|---|---|
| `USBSHARE_PASSWORD` | hasło wymagane od klientów |
| `USBSHARE_NAME` | nazwa serwera widoczna w kliencie |
| `USBSHARE_ALLOW` | udostępniaj tylko te urządzenia, np. `0403:6001,1-1.2` (VID:PID lub busid); puste = wszystkie |
| `USBSHARE_DENY` | nigdy nie udostępniaj, np. klawiatury serwera `046d:c52b` |
| `USBSHARE_LISTEN` | adres nasłuchu, domyślnie `:7600` (TCP) |

Huby USB nie są udostępniane. Urządzenie podłączone przez klienta jest odłączane od Linuksa i wraca do niego po rozłączeniu klienta.

## 2. Klient (Windows 10/11)

1. Zainstaluj sterownik **usbip-win2**, pobierając instalator z [github.com/vadimgrn/usbip-win2/releases](https://github.com/vadimgrn/usbip-win2/releases). Sterownik jest podpisany, więc nie trzeba włączać trybu testowego.
2. Pobierz klienta: [usbshare-client-windows-amd64.exe](https://github.com/smartPam/usbshare/releases/latest/download/usbshare-client-windows-amd64.exe) (zawsze najnowsza wersja).
3. Uruchom plik. Otworzy się okno w przeglądarce (`http://127.0.0.1:7601`).
4. Kliknij **Dodaj serwer**, wpisz IP serwera, port `7600` i hasło.
5. Przy wybranym urządzeniu kliknij **Podłącz**. Windows wykryje je jak zwykłe urządzenie USB.

Pozostałe funkcje:
- **auto:** urządzenie podłącza się samo, gdy tylko jest dostępne, także po restarcie.
- **Zwolnij:** rozłącza urządzenie używane przez inny komputer.
- **Ustawienia → Uruchamiaj razem z Windows:** klient startuje w tle po zalogowaniu.

Jeśli podłączanie zgłasza „odmowa dostępu”, uruchom klienta jako administrator.

Konfiguracja klienta jest w `%APPDATA%\USBShare\config.json`, a log w `client.log`.

## 3. Budowanie i wydania

GitHub Actions (`.github/workflows/build.yml`) przy każdym pushu do `main`:
- uruchamia testy,
- buduje obraz `ghcr.io/smartpam/usbshare-server:latest` dla amd64, arm64 i armv7, który serwery same pobiorą przy aktualizacji,
- publikuje wydanie **latest** z plikami `.exe` klienta i plikami serwera w zakładce **Releases**.

Wydanie z numerem wersji (opcjonalnie): `git tag v1.0.1 && git push --tags`.

> Za pierwszym razem ustaw pakiet `usbshare-server` jako **Public** (GitHub → Packages → usbshare-server → Package settings → Change visibility). Bez tego serwery nie pobiorą obrazu bez logowania.

Budowanie ręczne (Go 1.24+):

```bash
go test ./...
GOOS=windows GOARCH=amd64 go build -ldflags "-H=windowsgui" -o usbshare-client.exe ./cmd/usbshare-client
docker build -t usbshare-server .
```

## Jak to działa

1. Klient łączy się z serwerem przez TLS (port 7600) i loguje hasłem. Za pierwszym razem zapamiętuje odcisk certyfikatu, a jeśli ten się później zmieni, ostrzega.
2. Dla każdego serwera klient otwiera lokalny tunel `127.42.x.y:3240`.
3. `usbip.exe attach -r 127.42.x.y -b <busid>` (usbip-win2) łączy się z tunelem zwykłym USB/IP, a tunel przekazuje ruch do serwera przez TLS.
4. Serwer podpina urządzenie pod `usbip-host`, przekazuje jądru gniazdo i pośredniczy między nim a połączeniem TLS. Bloki URB obsługuje jądro.

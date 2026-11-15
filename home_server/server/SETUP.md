# Home server: on/off toggle, Radarr/Sonarr, phone access

## How it fits together

| Piece | Runs as | Goes through Surfshark? |
|---|---|---|
| Jellyfin | Mac app | No |
| OTA upscaler | launchd service | No |
| qBittorrent (does the downloading) | Docker, inside the VPN container | **Yes, always** |
| Radarr (movies), Sonarr (TV), Prowlarr (indexers) | Docker | No — they only search and send instructions |
| Tailscale (phone access) | Mac app | No |

The VPN lives inside a container (gluetun), so only qBittorrent uses it. If
Surfshark disconnects, qBittorrent is cut off from the internet instead of
falling back to your real connection. Don't run the Surfshark Mac app at the
same time — it isn't needed, and it can interfere with Tailscale.

Folder layout:

```
~/homey/home_server/
    ota_upscaler.py  com.home.ota-upscaler.plist  README.md
    server/
        server.sh  server.conf  SETUP.md
        arr-stack/  docker-compose.yml  .env
~/Media/
    downloads/   movies/   tv/
```

---

## 1. Install Docker

Install **Docker Desktop** (docker.com) or **OrbStack** (orbstack.dev — lighter on
a MacBook Air). If you use OrbStack, set `DOCKER_APP="OrbStack"` in `server.conf`.

## 2. Set up the Radarr/Sonarr stack

```
mkdir -p ~/Media/{downloads,movies,tv}
cd ~/homey/home_server/server/arr-stack
cp .env.example .env
open -e .env
```

Fill in your Surfshark WireGuard key (instructions are inside `.env`) and replace
`YOUR_USERNAME`. Then:

```
docker compose up -d
docker logs gluetun 2>&1 | grep -i "public ip"
```

The public IP shown should be a Surfshark address, not your home one.

Get qBittorrent's first-time password:

```
docker logs qbittorrent 2>&1 | grep -i password
```

### Connect the apps (one time)

Open each in a browser and create a login when it asks.

**qBittorrent** — http://localhost:8080
- Settings → Downloads → Default save path: `/data/downloads`
- Settings → Web UI → change the password.

**Prowlarr** — http://localhost:9696
- Add your indexers.
- Settings → Apps → add Radarr (`http://radarr:7878`) and Sonarr
  (`http://sonarr:8989`), with each app's API key (Settings → General in each).
  Prowlarr server: `http://prowlarr:9696`.

**Radarr** — http://localhost:7878 and **Sonarr** — http://localhost:8989
- Settings → Download Clients → **+** → qBittorrent
  - Host: `gluetun`, Port: `8080`, plus the qBittorrent login.
- Settings → Media Management → Root folder: `/data/movies` (Radarr) or
  `/data/tv` (Sonarr).

**Jellyfin** — add libraries pointing at `~/Media/movies` and `~/Media/tv`.

## 3. Set up the on/off toggle

```
cd ~/homey/home_server/server
chmod +x server.sh
cp server.conf.example server.conf
open -e server.conf
```

Paste a Jellyfin API key (Jellyfin → Dashboard → API Keys → **+**). This lets
"off" warn you about people watching and recordings starting soon.

Test it in Terminal:

```
./server.sh status
./server.sh on
./server.sh off
```

### Make it one click

In the **Shortcuts** app:

1. Shortcuts → Settings → Advanced → turn on **Allow Running Scripts**.
2. New shortcut → add **Run Shell Script** → enter `~/homey/home_server/server/server.sh on`.
   Name it **Server On**.
3. Duplicate it, change the command to `~/homey/home_server/server/server.sh off`, and name it
   **Server Off**.
4. For each one: open the shortcut's details (ⓘ) → **Pin in Menu Bar**.

**What "on" does:** starts Docker and the Radarr/Sonarr stack, the upscaler and
Jellyfin, and keeps the Mac awake while it's on the charger.

**What "off" does:** if someone is watching, or a recording is running or starts
within 2 hours, it asks first. Then it stops everything and lets the Mac sleep.
Downloads pause and resume the next time you turn the server on.

After a restart, click **Server On** once.

## 4. Phone access with Tailscale

1. Install **Tailscale** on the Mac and on your phone, and sign in to the same
   account on both.
2. In the Tailscale admin page, check the Mac's name (for example `macbook-air`).
3. On your phone, with Tailscale on, open:
   - Radarr: `http://macbook-air:7878`
   - Sonarr: `http://macbook-air:8989`
   - qBittorrent: `http://macbook-air:8080`
   - Jellyfin: `http://macbook-air:8096`
4. Use Share → **Add to Home Screen** on each so they open like apps.

This works on Wi-Fi or cellular, from anywhere. Nothing is exposed to the open
internet — don't set up port forwarding on your router.

### Turn the server on or off from your phone (optional)

1. On the Mac: System Settings → General → Sharing → turn on **Remote Login**.
2. On iPhone, in Shortcuts, add **Run Script Over SSH**:
   - Host: `macbook-air`, User: your Mac username, then your password or an SSH key.
   - Script: `~/homey/home_server/server/server.sh on` (or `off`).

From the phone, "off" can't show the warning, so it cancels if anything is in
use. Run `~/homey/home_server/server/server.sh status` first, and use
`~/homey/home_server/server/server.sh off --force` only when you're sure.

The Mac has to be awake to reach it. Once the server is off and the laptop is
asleep, you can't wake it from the phone.

---

## Storage note (Phase 1)

Downloads and your library share the Air's internal drive. In Radarr and Sonarr,
set Settings → Media Management → **Minimum free space** to about 20 GB so they
stop before the drive fills.

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
/Volumes/MyDrive/Media/        (your media drive; any folder works)
    downloads/   movies/   tv/   recordings/
```

Downloads and the library must be on the same drive, so finished downloads are
moved instantly (hardlinked) instead of copied.

---

## 1. Install Docker

Install **Docker Desktop** (docker.com) or **OrbStack** (orbstack.dev — lighter on
a MacBook Air). If you use OrbStack, set `DOCKER_APP="OrbStack"` in `server.conf`.

## 2. Set up the Radarr/Sonarr stack

Use your own media folder in the first line (here `/Volumes/MyDrive/Media`):

```
mkdir -p /Volumes/MyDrive/Media/{downloads,movies,tv}
cd ~/homey/home_server/server/arr-stack
cp .env.example .env
open -e .env
```

Fill in your Surfshark WireGuard key (instructions are inside `.env`) and set
`MEDIA_DIR` to the same media folder. If the media folder is on an external drive,
also set `MEDIA_DRIVE` in `server.conf` (step 3). Then:

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

**Jellyfin** — add libraries pointing at the `movies` and `tv` folders inside your
media folder. Don't add the media folder itself or `downloads`: Jellyfin would show
every film more than once (the hardlinked copy in `downloads` looks like a second
film).

## 3. Set up the on/off toggle

```
cd ~/homey/home_server/server
chmod +x server.sh
cp server.conf.example server.conf
open -e server.conf
```

If you installed the upscaler as a LaunchAgent before, remove it: `server.sh` now
starts the upscaler itself.

```
launchctl bootout gui/$(id -u)/com.home.ota-upscaler
rm ~/Library/LaunchAgents/com.home.ota-upscaler.plist
```

**Install the HDHomeRun relay (once, asks for your Mac password).** macOS does not
reliably let a background Python talk to devices on the home network ("No route to
host"), but system services running as root are exempt. The relay is a tiny system
service that passes connections from this Mac through to the HDHomeRun; the upscaler
talks to it on `127.0.0.1`. Use your HDHomeRun's name or reserved IP in the second line:

```
sudo install -d /usr/local/libexec
sudo install -o root -g wheel -m 755 hdhr_relay.py /usr/local/libexec/hdhr_relay.py
sed "s|__HDHR_HOST__|hdhomerun.local|" com.home.hdhr-relay.plist | sudo tee /Library/LaunchDaemons/com.home.hdhr-relay.plist >/dev/null
sudo launchctl bootstrap system /Library/LaunchDaemons/com.home.hdhr-relay.plist
curl -s http://127.0.0.1:8091/lineup.json | head -c 200
```

The last line should print the start of your channel list. The relay starts by itself
at every boot; its log is `/var/log/hdhr-relay.log`. After changing `hdhr_relay.py`,
run the second line again and then `sudo launchctl kickstart -k system/com.home.hdhr-relay`.
To remove it: `sudo launchctl bootout system/com.home.hdhr-relay`, then delete the two
installed files. (`HDHR_RELAY="no"` in `server.conf` makes the upscaler connect directly.)

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

**What "on" does:** checks that the media drive (`MEDIA_DRIVE` in `server.conf`)
is connected, and stops with a notification if it isn't. Then it starts Docker and
the Radarr/Sonarr stack, the upscaler and Jellyfin, keeps the Mac awake while it's
on the charger, and keeps the media drive from spinning down.

**What "off" does:** if someone is watching, or a recording is running or starts
within 2 hours, it asks first. Then it stops everything and lets the Mac sleep.
Downloads pause and resume the next time you turn the server on.

After a restart, click **Server On** once. The upscaler's log is
`~/Library/Logs/ota-upscaler.log` (`tail -f` it to watch); if the upscaler ever
stops on its own, it is started again after 10 seconds.

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

## Storage note

Downloads and your library share one drive. In Radarr and Sonarr, set Settings →
Media Management → **Minimum free space** (shown with "Show Advanced") to about
50 GB on a 1 TB drive so they stop before the drive fills. Keep the copies in
`downloads` while a torrent is still sharing: they are the same files as the
library copies and take no extra space.

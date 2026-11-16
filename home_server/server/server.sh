#!/bin/bash
# Home server toggle for the MacBook Air.
#   server.sh on      start everything and keep the Mac awake while plugged in
#   server.sh off     warn about anything in use, then stop everything
#   server.sh status  show what's running
# Settings live in server.conf next to this script.

set -u
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/Applications/Docker.app/Contents/Resources/bin:$HOME/.orbstack/bin"

DIR="$(cd "$(dirname "$0")" && pwd)"
CONF="$DIR/server.conf"
[ -f "$CONF" ] || { echo "Missing $CONF (copy server.conf.example)"; exit 1; }
# shellcheck source=/dev/null
source "$CONF"

: "${JELLYFIN_URL:=http://127.0.0.1:8096}"
: "${JELLYFIN_API_KEY:=}"
: "${JELLYFIN_APP:=Jellyfin}"
: "${HDHR_RELAY:=yes}"
: "${HDHR_HOST:=hdhomerun.local}"
: "${PYTHON:=/opt/homebrew/bin/python3}"
: "${MEDIA_DRIVE:=}"
: "${ARR_DIR:=$HOME/homey/home_server/server/arr-stack}"
: "${DOCKER_APP:=Docker}"
: "${WARN_MINUTES:=120}"

SELF="$DIR/server.sh"
UPSCALER="$(dirname "$DIR")/ota_upscaler.py"
UPSCALER_LOG="$HOME/Library/Logs/ota-upscaler.log"
UPSCALER_PID="$DIR/.upscaler.pid"     # the restart loop below, not Python itself
OLD_AGENT="$HOME/Library/LaunchAgents/com.home.ota-upscaler.plist"
AWAKE_PID="$DIR/.caffeinate.pid"
DRIVE_PID="$DIR/.drive-awake.pid"

notify() { osascript -e "display notification \"$1\" with title \"Home server\"" >/dev/null 2>&1; echo "$1"; }

docker_ready() { docker info >/dev/null 2>&1; }

# The Radarr/Sonarr stack counts as set up once its .env exists (SETUP.md step 2).
arr_ready() { [ -f "$ARR_DIR/.env" ]; }

start_docker() {
  docker_ready && return 0
  open -a "$DOCKER_APP" || return 1
  for _ in $(seq 1 60); do docker_ready && return 0; sleep 2; done
  return 1
}

# ---- Upscaler ------------------------------------------------------------- #
# server.sh starts the upscaler itself (not launchd), so it gets the Local Network
# permission of whatever ran this script: Shortcuts, Terminal or SSH. macOS keeps
# that permission reliably; for a background Python started by launchd it does not.

# With HDHR_RELAY=yes the upscaler reaches the HDHomeRun through hdhr_relay.py
# (a system service on 127.0.0.1, see SETUP.md), because macOS does not reliably let
# a background Python talk to devices on the home network.
relay_running() { nc -z 127.0.0.1 8091 >/dev/null 2>&1; }

upscaler_running() { pgrep -f "[o]ta_upscaler.py" >/dev/null; }

# Runs the upscaler and starts it again if it ever stops on its own. "off" stops this loop first.
upscaler_loop() {
  local tuner=(--hdhr "$HDHR_HOST")
  [ "$HDHR_RELAY" = "yes" ] && tuner=(--hdhr 127.0.0.1:8091 --stream-host 127.0.0.1:8092)
  while true; do
    "$PYTHON" "$UPSCALER" "${tuner[@]}" --max-streams 2 >>"$UPSCALER_LOG" 2>&1
    local code=$?
    echo "$(date '+%Y-%m-%d %H:%M:%S') upscaler stopped (exit code $code); starting it again in 10 s" >>"$UPSCALER_LOG"
    sleep 10
  done
}

start_upscaler() {
  upscaler_running && return 0
  nohup "$SELF" _upscaler_loop >/dev/null 2>&1 &
  echo $! > "$UPSCALER_PID"
  sleep 3
  upscaler_running
}

stop_upscaler() {
  if [ -f "$UPSCALER_PID" ]; then
    kill "$(cat "$UPSCALER_PID")" 2>/dev/null
    rm -f "$UPSCALER_PID"
  fi
  pkill -TERM -f "[o]ta_upscaler.py"            # lets it stop its AI workers cleanly
  for _ in $(seq 1 10); do upscaler_running || break; sleep 1; done
  pkill -KILL -f "[o]ta_upscaler.py"
  # Nothing of the upscaler may outlive "off": its AI workers and their ffmpeg
  # helpers (which read from pipes; Jellyfin's own ffmpeg does not) go too.
  pkill -KILL -f "enhance/[w]orker.py"
  pkill -KILL -f "enhance/[c]leanup_stage.py"
  pkill -KILL -f "[f]fmpeg .*pipe:[0-9]"
  return 0
}

# ---- Media drive ---------------------------------------------------------- #
# MEDIA_DRIVE (server.conf) is the external drive holding the Jellyfin libraries,
# e.g. /Volumes/Insignia1TB. Empty = media on the internal disk, no drive checks.

drive_mounted() { mount | grep -qF " on $MEDIA_DRIVE "; }

# Touches a tiny file every 2 minutes so the drive never spins down while the
# server is on (a spinning drive waking up can draw too much power over USB and drop out).
drive_awake_loop() {
  while true; do
    touch "$MEDIA_DRIVE/.keep-awake" 2>/dev/null
    sleep 120
  done
}

start_drive_awake() {
  [ -f "$DRIVE_PID" ] && kill -0 "$(cat "$DRIVE_PID")" 2>/dev/null && return 0
  nohup "$SELF" _drive_awake >/dev/null 2>&1 &
  echo $! > "$DRIVE_PID"
}

stop_drive_awake() {
  if [ -f "$DRIVE_PID" ]; then
    kill "$(cat "$DRIVE_PID")" 2>/dev/null
    rm -f "$DRIVE_PID"
  fi
}

# ---- Jellyfin checks ------------------------------------------------------ #

jellyfin_get() {
  [ -n "$JELLYFIN_API_KEY" ] || return 1
  curl -fsS --max-time 5 -H "Authorization: MediaBrowser Token=\"$JELLYFIN_API_KEY\"" "$JELLYFIN_URL$1"
}

# Prints one line per thing that would be interrupted.
in_use_report() {
  local sessions timers
  sessions="$(jellyfin_get "/Sessions?activeWithinSeconds=300")" || sessions="[]"
  timers="$(jellyfin_get "/LiveTv/Timers")" || timers='{"Items":[]}'
  SESSIONS="$sessions" TIMERS="$timers" WARN_MINUTES="$WARN_MINUTES" python3 - <<'PY'
import json, os, re
from datetime import datetime, timezone, timedelta

def parse(ts):
    # Jellyfin sends e.g. 2026-11-10T01:00:00.0000000Z (UTC)
    m = re.match(r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)", ts or "")
    return datetime.strptime(m.group(1), "%Y-%m-%dT%H:%M:%S").replace(tzinfo=timezone.utc) if m else None

try:
    for s in json.loads(os.environ["SESSIONS"]):
        item = s.get("NowPlayingItem")
        if item:
            print(f"{s.get('UserName','Someone')} is watching {item.get('Name','something')} on {s.get('DeviceName','a device')}")
except Exception:
    pass
try:
    now = datetime.now(timezone.utc)
    soon = now + timedelta(minutes=int(os.environ["WARN_MINUTES"]))
    for t in json.loads(os.environ["TIMERS"]).get("Items", []):
        start = parse(t.get("StartDate"))
        if not start:
            continue
        end = parse(t.get("EndDate")) or start
        if t.get("Status") == "InProgress" or start <= now <= end:
            print(f"Recording now: {t.get('Name','a show')}")
        elif now < start <= soon:
            mins = int((start - now).total_seconds() // 60)
            print(f"Recording in {mins} min: {t.get('Name','a show')}")
except Exception:
    pass
PY
}

# ---- Commands ------------------------------------------------------------- #

cmd_on() {
  local problems=()

  if [ -n "$MEDIA_DRIVE" ]; then
    if ! drive_mounted; then
      notify "Media drive not connected ($MEDIA_DRIVE): server not started"
      return 1
    fi
    start_drive_awake
  fi

  if arr_ready; then
    if start_docker; then
      (cd "$ARR_DIR" && docker compose up -d >/dev/null 2>&1) || problems+=("Radarr/Sonarr stack")
    else
      problems+=("Docker didn't start")
    fi
  fi

  if [ -f "$OLD_AGENT" ]; then
    problems+=("old upscaler LaunchAgent still installed (see SETUP.md step 3)")
  fi
  if [ "$HDHR_RELAY" = "yes" ] && ! relay_running; then
    problems+=("HDHomeRun relay not running (see SETUP.md step 3)")
  fi
  start_upscaler || problems+=("upscaler (see $UPSCALER_LOG)")

  open -a "$JELLYFIN_APP" || problems+=("Jellyfin")

  # Keep the Mac awake while on the charger (-s only applies on AC power).
  if ! { [ -f "$AWAKE_PID" ] && kill -0 "$(cat "$AWAKE_PID")" 2>/dev/null; }; then
    nohup caffeinate -s -i >/dev/null 2>&1 &
    echo $! > "$AWAKE_PID"
  fi

  if [ ${#problems[@]} -eq 0 ]; then
    notify "Server is ON"
  else
    notify "Server on, but these failed: ${problems[*]}"
    return 1
  fi
}

cmd_off() {
  local force="${1:-}" report
  if [ "$force" != "--force" ]; then
    report="$(in_use_report)"
    if [ -n "$report" ]; then
      osascript -e "display dialog \"Turning the server off will interrupt:\n\n$report\" \
        with title \"Home server\" buttons {\"Cancel\", \"Turn off anyway\"} \
        default button \"Cancel\" with icon caution" >/dev/null 2>&1 \
        || { echo "Cancelled"; return 2; }
    fi
  fi

  if arr_ready && docker_ready; then
    (cd "$ARR_DIR" && docker compose stop >/dev/null 2>&1)
  fi
  stop_upscaler
  osascript -e "quit app \"$JELLYFIN_APP\"" >/dev/null 2>&1
  stop_drive_awake

  if [ -f "$AWAKE_PID" ]; then
    kill "$(cat "$AWAKE_PID")" 2>/dev/null
    rm -f "$AWAKE_PID"
  fi
  notify "Server is OFF — the Mac can sleep normally"
}

cmd_status() {
  local s
  if pgrep -x "$JELLYFIN_APP" >/dev/null || pgrep -f "jellyfin" >/dev/null; then s="running"; else s="stopped"; fi
  echo "Jellyfin:     $s"
  upscaler_running && s="running" || s="stopped"
  echo "Upscaler:     $s"
  if [ "$HDHR_RELAY" = "yes" ]; then
    relay_running && s="running" || s="NOT running"
    echo "Tuner relay:  $s"
  fi
  if ! arr_ready; then
    echo "Docker stack: not set up"
  elif docker_ready; then
    echo "Docker stack:"
    (cd "$ARR_DIR" && docker compose ps --format '  {{.Service}}: {{.State}}')
  else
    echo "Docker stack: stopped"
  fi
  { [ -f "$AWAKE_PID" ] && kill -0 "$(cat "$AWAKE_PID")" 2>/dev/null; } && s="yes" || s="no"
  echo "Kept awake:   $s"
  if [ -n "$MEDIA_DRIVE" ]; then
    drive_mounted && s="connected" || s="NOT connected"
    { [ -f "$DRIVE_PID" ] && kill -0 "$(cat "$DRIVE_PID")" 2>/dev/null; } && s="$s, kept spinning" || s="$s, may spin down"
    echo "Media drive:  $s"
  fi
  local report; report="$(in_use_report)"
  [ -n "$report" ] && { echo "In use:"; echo "$report" | sed 's/^/  /'; }
  return 0
}

case "${1:-}" in
  on)     cmd_on ;;
  off)    cmd_off "${2:-}" ;;
  status) cmd_status ;;
  _upscaler_loop) upscaler_loop ;;     # used by "on"; not for typing by hand
  _drive_awake)   drive_awake_loop ;;  # used by "on"; not for typing by hand
  *)      echo "Usage: $0 on | off [--force] | status"; exit 1 ;;
esac

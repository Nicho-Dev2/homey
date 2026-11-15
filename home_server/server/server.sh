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
: "${UPSCALER_LABEL:=com.home.ota-upscaler}"
: "${ARR_DIR:=$HOME/homey/home_server/server/arr-stack}"
: "${DOCKER_APP:=Docker}"
: "${WARN_MINUTES:=120}"

UID_NUM="$(id -u)"
UPSCALER_PLIST="$HOME/Library/LaunchAgents/$UPSCALER_LABEL.plist"
AWAKE_PID="$DIR/.caffeinate.pid"

notify() { osascript -e "display notification \"$1\" with title \"Home server\"" >/dev/null 2>&1; echo "$1"; }

docker_ready() { docker info >/dev/null 2>&1; }

start_docker() {
  docker_ready && return 0
  open -a "$DOCKER_APP" || return 1
  for _ in $(seq 1 60); do docker_ready && return 0; sleep 2; done
  return 1
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

  if [ -f "$ARR_DIR/docker-compose.yml" ]; then
    if start_docker; then
      (cd "$ARR_DIR" && docker compose up -d >/dev/null 2>&1) || problems+=("Radarr/Sonarr stack")
    else
      problems+=("Docker didn't start")
    fi
  fi

  if [ -f "$UPSCALER_PLIST" ]; then
    launchctl print "gui/$UID_NUM/$UPSCALER_LABEL" >/dev/null 2>&1 \
      || launchctl bootstrap "gui/$UID_NUM" "$UPSCALER_PLIST" || problems+=("upscaler")
  fi

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

  if [ -f "$ARR_DIR/docker-compose.yml" ] && docker_ready; then
    (cd "$ARR_DIR" && docker compose stop >/dev/null 2>&1)
  fi
  launchctl bootout "gui/$UID_NUM/$UPSCALER_LABEL" >/dev/null 2>&1
  osascript -e "quit app \"$JELLYFIN_APP\"" >/dev/null 2>&1

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
  launchctl print "gui/$UID_NUM/$UPSCALER_LABEL" >/dev/null 2>&1 && s="running" || s="stopped"
  echo "Upscaler:     $s"
  if docker_ready && [ -f "$ARR_DIR/docker-compose.yml" ]; then
    echo "Docker stack:"
    (cd "$ARR_DIR" && docker compose ps --format '  {{.Service}}: {{.State}}')
  else
    echo "Docker stack: stopped"
  fi
  { [ -f "$AWAKE_PID" ] && kill -0 "$(cat "$AWAKE_PID")" 2>/dev/null; } && s="yes" || s="no"
  echo "Kept awake:   $s"
  local report; report="$(in_use_report)"
  [ -n "$report" ] && { echo "In use:"; echo "$report" | sed 's/^/  /'; }
  return 0
}

case "${1:-}" in
  on)     cmd_on ;;
  off)    cmd_off "${2:-}" ;;
  status) cmd_status ;;
  *)      echo "Usage: $0 on | off [--force] | status"; exit 1 ;;
esac

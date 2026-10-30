#!/usr/bin/env bash
# One launch/control directory for KoLmafia Xpra and host WaterSheep.
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
COMPOSE="$ROOT/compose.yaml"
OVERRIDE="$ROOT/compose.override.yaml"
RUN_DIR="$ROOT/watersheep"
PIDFILE="$RUN_DIR/watersheep.pid"
LOGFILE="$RUN_DIR/watersheep.log"
WS_BIN="${WATERSHEEP_BIN:-$HOME/anaconda3/bin/watersheep}"
WS_MODEL="${WATERSHEEP_MODEL:-samratduttaofficial/WaterSheep}"
WS_PORT="${WATERSHEEP_PORT:-8766}"

command -v docker >/dev/null || { echo 'Docker is not installed or not in PATH.' >&2; exit 1; }
command -v flock >/dev/null || { echo 'flock is required (util-linux).' >&2; exit 1; }
command -v ss >/dev/null || { echo 'ss is required (iproute2).' >&2; exit 1; }
[[ -f "$COMPOSE" && -f "$OVERRIDE" ]] || {
  echo "Expected compose.yaml and compose.override.yaml in $ROOT" >&2; exit 1;
}
mkdir -p "$RUN_DIR"
exec 9>"$RUN_DIR/.kolstack.lock"
flock -n 9 || { echo 'Another kolstack operation is in progress.' >&2; exit 1; }

compose() { docker compose --project-directory "$ROOT" -f "$COMPOSE" -f "$OVERRIDE" "$@"; }

ws_is_ours() {
  local pid
  [[ -s "$PIDFILE" ]] || return 1
  read -r pid < "$PIDFILE" || return 1
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  # Protect against a reused PID or a WaterSheep started by a different tool.
  ps -p "$pid" -o args= 2>/dev/null | grep -F -- "$WS_BIN" | grep -Fq -- "$WS_MODEL"
}

port_in_use() {
  # Refuse port collisions; a 404 at /health is not a reliable WaterSheep check.
  ss -H -ltn "( sport = :$WS_PORT )" 2>/dev/null | grep -q .
}

start_ws() {
  if ws_is_ours; then
    echo "WaterSheep already started by kolstack (PID $(cat "$PIDFILE"))."
    return 0
  fi
  if port_in_use; then
    echo "Port $WS_PORT is already in use. Not starting a duplicate WaterSheep." >&2
    echo "Inspect with: ss -ltnp '( sport = :$WS_PORT )'" >&2
    return 1
  fi
  [[ -x "$WS_BIN" ]] || {
    echo "WaterSheep executable not found: $WS_BIN" >&2
    echo 'Set WATERSHEEP_BIN=/full/path/to/watersheep if needed.' >&2
    return 1
  }
  # Work from this repo; use existing Hugging Face model cache (no duplicate copy).
  (
    cd "$ROOT"
    nohup "$WS_BIN" --model "$WS_MODEL" --device cpu --serve "$WS_PORT" \
      >"$LOGFILE" 2>&1 </dev/null 9>&- &
    echo "$!" > "$PIDFILE"
  )
  sleep 2
  if ws_is_ours; then
    echo "WaterSheep started (PID $(cat "$PIDFILE")); loading/listening on port $WS_PORT."
    echo "Log: $LOGFILE"
  else
    echo "WaterSheep exited or failed its startup check. Log: $LOGFILE" >&2
    tail -n 20 "$LOGFILE" >&2 || true
    return 1
  fi
}

stop_ws() {
  if ws_is_ours; then
    local pid
    read -r pid < "$PIDFILE"
    echo "Stopping WaterSheep PID $pid..."
    kill -TERM "$pid"
    for _ in {1..15}; do
      kill -0 "$pid" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then
      echo "WaterSheep did not exit after SIGTERM. Not using SIGKILL automatically." >&2
      return 1
    fi
    rm -f "$PIDFILE"
    echo 'WaterSheep stopped.'
  elif [[ -f "$PIDFILE" ]]; then
    echo 'Stored WaterSheep PID is no longer ours; not killing any process.'
    rm -f "$PIDFILE"
  else
    echo 'No WaterSheep process managed by kolstack.'
  fi
}

start_all() {
  if [[ ! -f "$ROOT/.env" ]]; then
    [[ -f "$ROOT/.env.example" ]] || { echo 'Missing .env.example' >&2; return 1; }
    cp "$ROOT/.env.example" "$ROOT/.env"
    echo 'Created .env from .env.example; review KOLMAFIA_DIR if you use a different data folder.'
  fi
  echo 'Starting KoLmafia / Xpra...'
  compose up -d
  start_ws
  echo 'Xpra: http://127.0.0.1:8080/xpra/'
  echo 'Relay: http://127.0.0.1:8080/'
}

case "${1:-}" in
  start) start_all ;;
  stop)
    ws_result=0
    stop_ws || ws_result=1
    echo 'Stopping KoLmafia / Xpra...'
    compose down
    exit "$ws_result"
    ;;
  restart) stop_ws; compose down; start_all ;;
  status)
    echo '=== KoLmafia / Xpra ==='
    compose ps
    echo '=== WaterSheep ==='
    if ws_is_ours; then
      echo "Managed PID: $(cat "$PIDFILE")"
    else
      echo 'No currently managed WaterSheep process.'
    fi
    if port_in_use; then echo "Port $WS_PORT: listening"; else echo "Port $WS_PORT: not listening (or still loading)"; fi
    ;;
  logs)
    echo '=== WaterSheep ==='
    [[ -f "$LOGFILE" ]] && tail -n 60 "$LOGFILE" || echo 'No WaterSheep log yet.'
    echo '=== KoLmafia / Xpra ==='
    compose logs --tail 60
    ;;
  *) echo "Usage: $(basename "$0") {start|stop|restart|status|logs}" >&2; exit 2 ;;
esac

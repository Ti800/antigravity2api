#!/bin/sh
# keepalive.sh - restart the service when the host environment recycles it.
#
# Observed on iOS/iSH (2026-09-22): the environment SIGKILLs Go processes about
# every 180 seconds. Confirmed across Go 1.23/1.26 toolchains, different binary
# locations and runtime flags; python/shell processes are unaffected. Shell
# loops survive, so this watchdog brings the service back within seconds:
#
#   nohup sh scripts/keepalive.sh >/dev/null 2>&1 &
#
# Place this next to the binary (same directory as service.sh) or run it from
# scripts/ with the deployment directory layout. Skip it on environments that
# do not show the recycling behaviour.
DIR=$(cd "$(dirname "$0")" && pwd)
LOG="$DIR/keepalive.log"
while true; do
  PORT=$(cat "$DIR/server.port" 2>/dev/null || echo 8081)
  if ! curl -sS -m 3 -o /dev/null "http://127.0.0.1:$PORT/healthz" 2>/dev/null; then
    echo "$(date '+%H:%M:%S') service down -> restarting" >> "$LOG"
    sh "$DIR/service.sh" start >> "$LOG" 2>&1
  fi
  sleep 5
done

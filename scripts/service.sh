#!/bin/sh
# Bounded service manager for iOS/iSH. No watchdog: iOS kills the whole
# environment, so a daemon inside it cannot outlive an app reclaim.
umask 077
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1
DIR=${DIR:-$SCRIPT_DIR}; DIR=$(CDPATH= cd -- "$DIR" && pwd -P) || exit 1
BIN="$DIR/gemini-web2api-ios"
PIDFILE="$DIR/server.pid"
PORTFILE="$DIR/server.port"
LOCK="$DIR/.service.lock"
CONF="$DIR/config.json"
PORTS=${PORTS:-8081 8082 8083}

valid_pid() { case "$1" in ''|*[!0-9]*|0|1) return 1;; esac; }
valid_port() { case "$1" in ''|*[!0-9]*|0*|??????*) return 1;; esac; [ "$1" -le 65535 ]; }
owned() {
	valid_pid "$1" || return 1
	kill -0 "$1" 2>/dev/null || return 1
	if [ -r "/proc/$1/cmdline" ]; then
		first=$(tr '\000' '\n' < "/proc/$1/cmdline" | sed -n '1p')
		[ "$first" = "$BIN" ]
	fi
}
is_running() { pid=$(cat "$PIDFILE" 2>/dev/null) || return 1; owned "$pid"; }

atomic_write() {
	tmp=$(mktemp "$DIR/.service-write.XXXXXX") || return 1
	if printf '%s\n' "$2" > "$tmp" && mv -f "$tmp" "$1"; then return 0; fi
	rm -f "$tmp"; return 1
}
release_lock() { [ "$(cat "$LOCK/owner" 2>/dev/null)" = "$$" ] || return; rm -f "$LOCK/owner"; rmdir "$LOCK" 2>/dev/null || :; }
acquire_lock() {
	n=0
	while [ "$n" -lt 10 ]; do
		if mkdir "$LOCK" 2>/dev/null; then
			printf '%s\n' "$$" > "$LOCK/owner" || { rmdir "$LOCK"; return 1; }
			trap release_lock EXIT
			return 0
		fi
		n=$((n+1)); sleep 1
	done
	echo 'ERROR: service lock busy' >&2
	return 1
}

# curl exit 7 is connection refused (free). A hang is a zombie socket left by an
# app reclaim; skip it instead of binding and failing.
probe() {
	health_pid=
	state=busy
	response=$(curl --noproxy '*' -sS --connect-timeout 1 --max-time 2 --max-filesize 4096 \
		-w '\n%{http_code}' "http://127.0.0.1:$1/healthz" 2>/dev/null)
	rc=$?
	[ "$rc" -eq 7 ] && { state=free; return; }
	[ "$rc" -eq 0 ] || return
	code=$(printf '%s\n' "$response" | tail -n1)
	[ "$code" = 200 ] || return
	health_pid=$(printf '%s\n' "$response" | sed '$d' | sed -n 's/.*"pid"[ ]*:[ ]*\([1-9][0-9]*\).*/\1/p')
	[ -n "$health_pid" ] && state=healthy
}

healthy() {
	is_running || return 1
	expected=$pid
	port=$(cat "$PORTFILE" 2>/dev/null) || return 1
	valid_port "$port" || return 1
	probe "$port"
	[ "$state" = healthy ] && [ "$health_pid" = "$expected" ]
}

stop_service() {
	if ! is_running; then rm -f "$PIDFILE"; echo 'not running'; return 0; fi
	kill -TERM "$pid" 2>/dev/null || return 1
	n=0
	while owned "$pid"; do
		[ "$n" -lt 10 ] || { echo 'ERROR: stop timed out' >&2; return 1; }
		n=$((n+1)); sleep 1
	done
	rm -f "$PIDFILE"
	echo "stopped: $pid"
}

start_service() {
	if healthy; then echo "ready: pid $pid on port $port"; return 0; fi
	if is_running; then stop_service || return $?; else rm -f "$PIDFILE"; fi
	[ -x "$BIN" ] && [ -f "$CONF" ] || { echo 'ERROR: binary or config missing' >&2; return 1; }
	chosen=
	previous=$(cat "$PORTFILE" 2>/dev/null)
	for p in $previous $PORTS; do
		valid_port "$p" || continue
		probe "$p"
		case "$state" in
			healthy) echo "ready: adopted pid $health_pid on port $p"; return 0;;
			free) [ -n "$chosen" ] || chosen=$p;;
		esac
	done
	[ -n "$chosen" ] || { echo 'ERROR: no usable port' >&2; return 1; }
	touch "$DIR/server.log" && chmod 600 "$DIR/server.log" || return 1
	cd "$DIR" || return 1
	# Local-only credentials. The file is gitignored; the process needs the
	# OAuth client id and secret to refresh tokens.
	[ -f "$DIR/.env" ] && . "$DIR/.env"
	nohup "$BIN" -port "$chosen" -config "$CONF" >> "$DIR/server.log" 2>&1 &
	child=$!
	atomic_write "$PIDFILE" "$child" && atomic_write "$PORTFILE" "$chosen" || return 1
	n=0
	while [ "$n" -lt 10 ]; do
		if healthy; then echo "ready: pid $child on port $chosen"; return 0; fi
		kill -0 "$child" 2>/dev/null || break
		n=$((n+1)); sleep 1
	done
	echo 'ERROR: service failed readiness; check server.log' >&2
	return 1
}

case "${1:-}" in start|stop|status|restart) ;; *) echo "usage: $0 {start|stop|status|restart}" >&2; exit 2;; esac
acquire_lock || exit $?
case "$1" in
	start) start_service;;
	stop) stop_service;;
	status) healthy && echo "ready: pid $pid on port $port" || { echo 'not healthy' >&2; exit 1; };;
	restart) stop_service && start_service;;
esac

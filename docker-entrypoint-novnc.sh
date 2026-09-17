#!/bin/sh
set -eu

display="${DISPLAY:-:99}"
screen="${XHS_NOVNC_SCREEN:-1920x1080x24}"

Xvfb "$display" -screen 0 "$screen" -ac -nolisten tcp >/tmp/xvfb.log 2>&1 &
xvfb_pid=$!

cleanup() {
  kill "$websockify_pid" "$vnc_pid" "$openbox_pid" "$xvfb_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Wait until X accepts clients before starting the window manager and VNC.
attempt=0
while ! xdpyinfo -display "$display" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 100 ]; then
    echo "Xvfb did not become ready" >&2
    exit 1
  fi
  sleep 0.1
done

DISPLAY="$display" openbox >/tmp/openbox.log 2>&1 &
openbox_pid=$!

if [ -n "${XHS_NOVNC_PASSWORD_FILE:-}" ]; then
  password="$(cat "$XHS_NOVNC_PASSWORD_FILE")"
  x11vnc -storepasswd "$password" /tmp/xhs-vnc.pass >/dev/null
  auth_args="-rfbauth /tmp/xhs-vnc.pass"
else
  # Development fallback: the caller must keep the HTTP/WebSocket route on loopback.
  auth_args="-nopw"
fi

# shellcheck disable=SC2086
x11vnc -display "$display" -forever -shared -rfbport 5900 $auth_args >/tmp/x11vnc.log 2>&1 &
vnc_pid=$!
websockify --web=/usr/share/novnc 6080 localhost:5900 >/tmp/websockify.log 2>&1 &
websockify_pid=$!

export DISPLAY="$display"
exec /usr/bin/tini -s -- "$@"

#!/bin/sh
# Runs the server install line in a clean Ubuntu container, which has no systemd (like Docker, WSL and small VPS
# images), against the fake relay: as root and as a normal user, it must install, pair, keep running in the
# background and come back when the Link is killed. Needs Go and Docker. Usage: scripts/test-install-no-systemd.sh
set -eu

image=${IMAGE:-ubuntu:24.04}
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
chmod 0755 "$work"
relay_pid=
files_pid=
name=
cleanup() {
  [ -z "$name" ] || docker rm -f "$name" >/dev/null 2>&1 || true
  [ -z "$relay_pid" ] || kill "$relay_pid" 2>/dev/null || true
  [ -z "$files_pid" ] || kill "$files_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

# The release assets install.sh downloads, served on loopback.
mkdir "$work/dl" "$work/x"
(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$work/x/firmary-link" ./cmd/firmary-link &&
  go build -o "$work/fake-relay" ./cmd/fake-relay)
tar -czf "$work/dl/firmary-link-linux-x64.tar.gz" -C "$work/x" firmary-link
(cd "$work/dl" && sha256sum firmary-link-linux-x64.tar.gz >checksums.txt)
cp "$root/packaging/install.sh" "$work/dl/"
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
python3 -m http.server "$port" --bind 127.0.0.1 --directory "$work/dl" >/dev/null 2>&1 &
files_pid=$!

# One fake relay per run; it confirms a pairing as soon as the code is claimed.
start_relay() {
  : >"$work/relay.out"
  tail -f /dev/null | "$work/fake-relay" -ticket-key-file "$work/ticket.pub" >"$work/relay.out" 2>&1 &
  relay_pid=$!
  for _ in $(seq 50); do grep -q '^pairing code:' "$work/relay.out" && break; sleep 0.1; done
  chmod 0644 "$work/ticket.pub" # for the normal user's run
  relay_url=$(sed -n 's/^fake relay: //p' "$work/relay.out")
  code=$(sed -n 's/^pairing code: \([A-Z0-9-]*\).*/\1/p' "$work/relay.out")
}

hellos() { grep -c '^hello:' "$work/relay.out" || true; }

# check USER [cron]: install as USER (root or a new account) in a fresh container and check it keeps running; with
# cron, also that it starts again when the server restarts.
check() {
  start_relay
  name=nosystemd-$1-$$
  # The Link's own downloads (Claude Code, Codex) go nowhere: the proxy refuses them, so the run stays light.
  docker run -d --rm --name "$name" --network host -v "$work:$work:ro" "$image" sleep 600 >/dev/null
  docker exec "$name" sh -c 'apt-get -qq update >/dev/null && apt-get -qq install -y curl procps >/dev/null'
  if [ "${2:-}" = cron ]; then
    docker exec "$name" sh -c 'apt-get -qq install -y cron >/dev/null && cron'
  fi
  run_as=root
  if [ "$1" != root ]; then
    docker exec "$name" useradd -m "$1"
    run_as=$1
  fi
  echo "--- install as $1 (no systemd in the container)"
  docker exec -u "$run_as" -e HTTPS_PROXY=http://127.0.0.1:9 -e FIRMARY_LINK_BASE_URL="http://127.0.0.1:$port" "$name" \
    sh -c "cd && curl -fsSL http://127.0.0.1:$port/install.sh | sh -s -- --code $code --env local \
      --relay-url $relay_url --ticket-key-file $work/ticket.pub"
  echo "--- status"
  docker exec -u "$run_as" "$name" sh -c "cd && PATH=\$PATH:\$HOME/.local/bin firmary-link status --env local \
    --relay-url $relay_url --ticket-key-file $work/ticket.pub" | grep -E 'Pairing|Service' | tee "$work/status"
  grep -q 'Pairing:  paired' "$work/status"
  grep -q 'Service:  running (background process)' "$work/status"
  # Paired, then the background Link reconnects on its own.
  grep -q 'pairing claimed and confirmed' "$work/relay.out"
  for _ in $(seq 100); do [ "$(hellos)" -gt 0 ] && break; sleep 0.1; done
  [ "$(hellos)" -gt 0 ] || { echo "the background Link never said hello"; exit 1; }
  echo "--- kill the Link; the supervisor starts it again"
  before=$(hellos)
  docker exec "$name" pkill -f "firmary-link run"
  for _ in $(seq 200); do [ "$(hellos)" -gt "$before" ] && break; sleep 0.1; done
  [ "$(hellos)" -gt "$before" ] || { echo "the Link didn't come back"; exit 1; }
  if [ "${2:-}" = cron ]; then
    echo "--- restart: stop everything, then start cron as at boot"
    before=$(hellos)
    docker exec "$name" sh -c 'pkill -f "firmary-link ([s]upervise|[r]un)"; pkill -x cron; sleep 1
      rm -f /run/crond.reboot; cron'
    for _ in $(seq 200); do [ "$(hellos)" -gt "$before" ] && break; sleep 0.1; done
    [ "$(hellos)" -gt "$before" ] || { echo "cron didn't start it again"; exit 1; }
  fi
  docker rm -f "$name" >/dev/null
  name=
  kill "$relay_pid"
  relay_pid=
  echo "ok  $1"
}

check root
check dana
check root cron

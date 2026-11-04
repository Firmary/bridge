#!/bin/sh
# Install Firmary Link on a Linux server or a Mac, for the current user, without sudo. Firmary (Settings ›
# Connections › Add a server) gives the line, with the pairing code it shows:
#
#   curl -fsSL https://github.com/Firmary/bridge/releases/latest/download/install.sh | sh -s -- --code ABCD-EFGH
#
# It downloads the latest release, checks it against checksums.txt, installs it, makes it start with the machine
# and keep running after you log out, pairs it with that code (you confirm in Firmary) and offers to sign in to
# Claude or ChatGPT. Without --code it asks for the code on a terminal.
#
# Options: --code ABCD-EFGH; for development also --env, --relay-url, --ticket-key-file (passed to firmary-link).
# FIRMARY_LINK_NONINTERACTIVE=1  don't ask anything here; print the commands instead
# FIRMARY_LINK_NO_SERVICE=1      don't make it start with the machine
# FIRMARY_LINK_BASE_URL          download from somewhere else (https, or http on localhost for tests)
set -eu

# Everything runs from main, so a download cut short by the network can't run half a script.
main() {
  code=
  link_args=
  while [ $# -gt 0 ]; do
    case "$1" in
      --code | --env | --relay-url | --ticket-key-file)
        [ $# -ge 2 ] || fail "$1 needs a value"
        # Values are passed on unquoted, so allow only what a code, environment, loopback URL or path holds.
        case "$2" in
          "" | -* | *[!A-Za-z0-9._:/@+-]*) fail "$1 $2 isn't valid" ;;
        esac
        if [ "$1" = --code ]; then code=$2; else link_args="$link_args $1 $2"; fi
        shift 2
        ;;
      *) fail "unknown option $1 (use --code ABCD-EFGH)" ;;
    esac
  done

  base_url=${FIRMARY_LINK_BASE_URL:-https://github.com/Firmary/bridge/releases/latest/download}
  base_url=${base_url%/}
  case "$base_url" in
    https://*) secure=1 ;;
    http://127.0.0.1:* | http://127.0.0.1/* | http://localhost:* | http://localhost/*) secure=0 ;;
    *) fail "FIRMARY_LINK_BASE_URL must be an https:// address" ;;
  esac

  os=$(uname -s)
  arch=$(uname -m)
  case "$os" in
    Linux)
      case "$arch" in
        x86_64 | amd64) asset=firmary-link-linux-x64.tar.gz ;;
        aarch64 | arm64) asset=firmary-link-linux-arm64.tar.gz ;;
        *) fail "Firmary Link runs on 64-bit Intel/AMD (x86_64) or Arm (aarch64) Linux; this machine is $arch" ;;
      esac
      ;;
    Darwin) asset=firmary-link-darwin-universal ;;
    *) fail "this installer is for Linux and macOS. On Windows, run Firmary-Link-Setup.exe from https://github.com/Firmary/bridge/releases/latest" ;;
  esac

  command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 ||
    fail "this installer needs sha256sum or shasum to check the download"

  tmp=$(mktemp -d 2>/dev/null || mktemp -d -t firmary-link)
  trap 'rm -rf "$tmp"' EXIT
  trap 'exit 1' HUP INT TERM

  download "$base_url/$asset" "$tmp/$asset"
  download "$base_url/checksums.txt" "$tmp/checksums.txt"

  expected=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/checksums.txt")
  [ -n "$expected" ] || fail "checksums.txt has no line for $asset; not installing"
  actual=$(sha256 "$tmp/$asset")
  [ "$actual" = "$expected" ] || fail "$asset doesn't match its checksum in checksums.txt; not installing"

  case "$asset" in
    *.tar.gz)
      mkdir "$tmp/x"
      tar -xzf "$tmp/$asset" -C "$tmp/x" firmary-link
      new="$tmp/x/firmary-link"
      ;;
    *) new="$tmp/$asset" ;;
  esac

  if [ "$(id -u)" -eq 0 ]; then
    dir=/usr/local/bin
  else
    dir="$HOME/.local/bin"
  fi
  mkdir -p "$dir"
  bin="$dir/firmary-link"
  # Copy next to the target and rename over it, so a copy that is running keeps working until it restarts.
  cp "$new" "$dir/.firmary-link.new"
  chmod 0755 "$dir/.firmary-link.new"
  mv -f "$dir/.firmary-link.new" "$bin"

  user=$(id -un 2>/dev/null || printf '%s' "${USER:-you}")
  say "ok  Installed for $user in $bin"

  # The commands to show the person: the bare name if it's on PATH, otherwise the full path.
  case ":${PATH:-}:" in
    *":$dir:"*) cmd=firmary-link ;;
    *)
      cmd=$bin
      say "    $dir isn't on your PATH. To run firmary-link by name, add it:"
      say "    export PATH=\"$dir:\$PATH\""
      ;;
  esac

  # curl's pipe is stdin, so anything interactive reads from the terminal instead.
  interactive=0
  if [ -z "${FIRMARY_LINK_NONINTERACTIVE:-}" ] && [ -t 1 ] && (: </dev/tty) 2>/dev/null; then
    interactive=1
  fi

  # Always on first, so the pairing below is for the server it will be (pair pauses the service while it runs).
  service=0
  if [ "${FIRMARY_LINK_NO_SERVICE:-}" != 1 ]; then
    # shellcheck disable=SC2086 # link_args is checked above: words without spaces or globs
    "$bin" service install $link_args
    service=1
  fi

  # The code came with the line, so pairing needs no terminal; one is used only if Firmary then asks this computer.
  tty=/dev/null
  if [ "$interactive" -eq 1 ]; then tty=/dev/tty; fi
  if [ -n "$code" ]; then
    # shellcheck disable=SC2086
    if ! "$bin" pair --code "$code" $link_args <"$tty"; then
      say ""
      say "Pairing didn't finish. Show a new code in Firmary, then run: $cmd pair --code ABCD-EFGH"
      exit 1
    fi
  elif [ "$interactive" -eq 1 ]; then
    # shellcheck disable=SC2086
    if ! "$bin" pair $link_args </dev/tty; then
      say ""
      say "Pairing didn't finish. Show a new code in Firmary, then run: $cmd pair --code ABCD-EFGH"
      exit 1
    fi
  else
    say ""
    say "Pair it with the code Firmary shows (Settings › Connections › Add a server):"
    say "    $cmd pair --code ABCD-EFGH"
  fi

  if [ "$interactive" -eq 1 ]; then
    sign_in
  else
    say ""
    say "Sign in to Claude or ChatGPT with one of:"
    say "    $cmd sign-in claude"
    say "    $cmd sign-in chatgpt"
    say "It shows a link that works on any browser, even a phone."
  fi

  say ""
  if [ "$service" -eq 1 ]; then
    say "You can close this window; it keeps running."
  else
    say "Start it with: $cmd run"
  fi
}

sign_in() {
  while :; do
    printf 'Sign in to Claude or ChatGPT now? [claude/chatgpt/skip] '
    answer=
    read -r answer </dev/tty || answer=skip
    answer=$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')
    case "$answer" in
      claude | chatgpt)
        # shellcheck disable=SC2086
        if ! "$bin" sign-in "$answer" --device $link_args </dev/tty; then
          say "Sign-in didn't finish. Try again any time: $cmd sign-in $answer"
        fi
        return 0
        ;;
      skip | s | no | n | "")
        say "Sign in later with: $cmd sign-in claude   or   $cmd sign-in chatgpt"
        return 0
        ;;
      *) say "Type claude, chatgpt or skip." ;;
    esac
  done
}

download() {
  if command -v curl >/dev/null 2>&1; then
    if [ "$secure" -eq 1 ]; then
      curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1" || fail "couldn't download $1"
    else
      curl -fsSL -o "$2" "$1" || fail "couldn't download $1"
    fi
  elif command -v wget >/dev/null 2>&1; then
    # BusyBox wget has no --https-only; the https URL and the checksum still hold.
    if [ "$secure" -eq 1 ] && wget --help 2>&1 | grep -q -- --https-only; then
      wget -q --https-only -O "$2" "$1" || fail "couldn't download $1"
    else
      wget -q -O "$2" "$1" || fail "couldn't download $1"
    fi
  else
    fail "this installer needs curl or wget"
  fi
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

say() {
  printf '%s\n' "$*"
}

fail() {
  printf 'error  %s\n' "$*" >&2
  exit 1
}

main "$@"

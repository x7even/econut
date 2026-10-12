#!/usr/bin/env bash
# Install the econut-guard shutdown path: udev rule, NUT config, binary, systemd unit.
#
#   sudo deploy/install.sh [--dry-run] [--enable-service] [--uninstall]
#
# Safe by design: the installed service runs in dry-run (log only) mode. This
# script never adds --enable-shutdown; see deploy/README.md for that step.
# Re-running is fine: unchanged files are left alone, changed ones are backed up.
set -euo pipefail

DRY=0 ENABLE=0 UNINSTALL=0
for a in "$@"; do
  case "$a" in
    --dry-run) DRY=1 ;;
    --enable-service) ENABLE=1 ;;
    --uninstall) UNINSTALL=1 ;;
    -h|--help) sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $a (try --help)" >&2; exit 2 ;;
  esac
done

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(dirname "$HERE")
BIN="$ROOT/bin/econut-guard"
STAMP=$(date +%Y%m%d%H%M%S)
UPS_NAME=ecoflow

say()  { printf '==> %s\n' "$*"; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
run()  { if [ "$DRY" = 1 ]; then printf '[dry-run] %s\n' "$*"; else "$@"; fi; }

if [ "$DRY" = 0 ] && [ "$(id -u)" -ne 0 ]; then
  die "must run as root: sudo $0 $*"
fi

# install_file SRC DEST MODE [OWNER:GROUP]
# Copies only if content differs; backs up whatever it replaces.
install_file() {
  local src=$1 dest=$2 mode=$3 own=${4:-root:root}
  if [ -f "$dest" ] && cmp -s "$src" "$dest"; then
    say "unchanged: $dest"
    return 1
  fi
  if [ -f "$dest" ]; then
    run cp -p "$dest" "$dest.bak.$STAMP"
    say "backed up $dest -> $dest.bak.$STAMP"
  fi
  run install -m "$mode" -o "${own%%:*}" -g "${own##*:}" "$src" "$dest"
  say "installed: $dest"
}

if [ "$UNINSTALL" = 1 ]; then
  say "uninstalling (leaving /etc/nut alone: other UPSes may use it)"
  run systemctl disable --now econut-guard.service 2>/dev/null || true
  run rm -f /etc/systemd/system/econut-guard.service /usr/local/bin/econut-guard \
            /etc/udev/rules.d/99-ecoflow-ups.rules
  run systemctl daemon-reload
  run udevadm control --reload
  say "done. To remove NUT config too, edit/remove ups.conf, upsd.users in /etc/nut by hand."
  exit 0
fi

# ---- preflight --------------------------------------------------------------
[ -x "$BIN" ] || die "$BIN not found; build it first (as your normal user):
    cd $ROOT && go build -o bin/econut-guard ./cmd/econut-guard"
[ -x /lib/nut/usbhid-ups ] || [ -x /usr/lib/nut/usbhid-ups ] ||
  die "NUT is not installed; on Debian/Ubuntu: sudo apt install nut-server"
getent group nut >/dev/null || die "group 'nut' missing; is the nut-server package installed?"

# ---- 1. udev rule -----------------------------------------------------------
say "udev rule (lets group nut open the EcoFlow USB device)"
if install_file "$HERE/99-ecoflow-ups.rules" /etc/udev/rules.d/99-ecoflow-ups.rules 0644; then
  run udevadm control --reload
  run udevadm trigger --subsystem-match=usb --attr-match=idVendor=3746
fi

# ---- 2. NUT config ----------------------------------------------------------
say "NUT config in /etc/nut"
run install -d -m 0755 /etc/nut
changed=0
install_file "$HERE/nut.conf"   /etc/nut/nut.conf   0640 root:nut && changed=1
install_file "$HERE/ups.conf"   /etc/nut/ups.conf   0640 root:nut && changed=1
install_file "$HERE/upsd.conf"  /etc/nut/upsd.conf  0640 root:nut && changed=1

# upsd.users holds a password: generate one rather than shipping a default, and
# keep an existing install's password so re-runs don't churn it.
if [ -f /etc/nut/upsd.users ] && grep -q '^\[monitor\]' /etc/nut/upsd.users 2>/dev/null; then
  say "unchanged: /etc/nut/upsd.users (monitor user already present)"
else
  tmp=$(mktemp)
  trap 'rm -f "$tmp"' EXIT
  pw=$(head -c 24 /dev/urandom | base64 | tr -d '/+=\n')
  sed "s/CHANGE-ME-before-installing/$pw/" "$HERE/upsd.users" >"$tmp"
  install_file "$tmp" /etc/nut/upsd.users 0640 root:nut && changed=1
fi

if [ "$changed" = 1 ]; then
  say "restarting NUT"
  run systemctl restart nut-driver-enumerator.service 2>/dev/null ||
    say "note: nut-driver-enumerator not present, continuing"
  run systemctl restart "nut-driver@$UPS_NAME.service" 2>/dev/null || true
  run systemctl restart nut-server.service
fi

# ---- 3. binary + unit -------------------------------------------------------
say "binary and systemd unit (dry-run mode)"
install_file "$BIN" /usr/local/bin/econut-guard 0755 || true
if install_file "$HERE/econut-guard.service" /etc/systemd/system/econut-guard.service 0644; then
  run systemctl daemon-reload
fi
if [ "$ENABLE" = 1 ]; then
  run systemctl enable --now econut-guard.service
  say "econut-guard enabled in DRY-RUN mode (logs only, will not shut down)"
else
  say "econut-guard installed but not enabled (use --enable-service for the dry-run daemon)"
fi

# ---- 4. verify --------------------------------------------------------------
if [ "$DRY" = 0 ]; then
  say "checking the UPS through NUT"
  sleep 3
  if /usr/local/bin/econut-guard --once; then
    say "OK: econut-guard can read the EcoFlow through NUT"
  else
    die "econut-guard --once failed; check: systemctl status nut-server nut-driver@$UPS_NAME; upsc $UPS_NAME@localhost"
  fi
fi

cat <<EOF

Done. Real shutdown is NOT enabled. Watch the dry-run logs through a real outage
first:  journalctl -u econut-guard -f
Then see deploy/README.md for enabling it.
EOF

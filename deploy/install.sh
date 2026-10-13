#!/usr/bin/env bash
# Install econut: udev rule, NUT config, binaries, config file, systemd unit.
#
#   sudo deploy/install.sh [--shutdown-below PERCENT] [--dry-run] [--enable-service] [--uninstall]
#
#   --shutdown-below N  shut down when on battery and charge is below N percent
#                      (default 55; saved in /etc/econut/econut.conf)
#   --dry-run          print what would be done, change nothing
#   --enable-service   also enable and start the guard (arms the real shutdown)
#   --uninstall        remove the unit, binaries and udev rule (keeps /etc/nut, /etc/econut)
#
# The installed service is LIVE: once running it powers the machine off when the
# condition is met (on battery and below the level for the debounce period).
# Re-running is fine: unchanged files are left alone, changed ones are backed up.
set -euo pipefail

DRY=0 ENABLE=0 UNINSTALL=0 TRIGGER="" TRIGGER_SET=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    --enable-service) ENABLE=1 ;;
    --uninstall) UNINSTALL=1 ;;
    --shutdown-below) [ $# -ge 2 ] || { echo "--shutdown-below needs a value" >&2; exit 2; }
                     TRIGGER=$2; TRIGGER_SET=1; shift ;;
    --shutdown-below=*) TRIGGER=${1#*=}; TRIGGER_SET=1 ;;
    -h|--help) sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option: $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(dirname "$HERE")
STAMP=$(date +%Y%m%d%H%M%S)
UPS_NAME=ecoflow
CONF=/etc/econut/econut.conf

say()  { printf '==> %s\n' "$*"; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
run()  { if [ "$DRY" = 1 ]; then printf '[dry-run] %s\n' "$*"; else "$@"; fi; }

if [ "$TRIGGER_SET" = 1 ]; then
  # A number in (0, 100]; the guard validates again, but fail before touching anything.
  printf '%s' "$TRIGGER" | grep -Eq '^[0-9]+(\.[0-9]+)?$' &&
    awk -v v="$TRIGGER" 'BEGIN { exit !(v > 0 && v <= 100) }' ||
    die "--shutdown-below must be a number above 0 and at most 100, got '$TRIGGER'"
fi

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

remove_old_names() {
  # Earlier name of this project.
  if [ -e /etc/systemd/system/ups-guard.service ] || [ -e /usr/local/bin/ups-guard ]; then
    say "removing the old ups-guard install"
    run systemctl disable --now ups-guard.service 2>/dev/null || true
    run rm -f /etc/systemd/system/ups-guard.service /usr/local/bin/ups-guard
    run systemctl daemon-reload
  fi
}

if [ "$UNINSTALL" = 1 ]; then
  say "uninstalling (leaving /etc/nut and /etc/econut alone)"
  run systemctl disable --now econut-guard.service 2>/dev/null || true
  run rm -f /etc/systemd/system/econut-guard.service /usr/local/bin/econut-guard \
            /usr/local/bin/econut /etc/udev/rules.d/99-ecoflow-ups.rules
  remove_old_names
  run systemctl daemon-reload
  run udevadm control --reload
  say "done."
  exit 0
fi

# ---- preflight --------------------------------------------------------------
for b in econut-guard econut; do
  [ -x "$ROOT/bin/$b" ] || die "$ROOT/bin/$b not found; build first (as your normal user):
    cd $ROOT && go build -o bin/ ./cmd/..."
done
[ -x /lib/nut/usbhid-ups ] || [ -x /usr/lib/nut/usbhid-ups ] ||
  die "NUT is not installed; on Debian/Ubuntu: sudo apt install nut-server"
getent group nut >/dev/null || die "group 'nut' missing; is the nut-server package installed?"

remove_old_names

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

# ---- 3. econut config -------------------------------------------------------
say "econut config ($CONF)"
run install -d -m 0755 /etc/econut
if [ ! -f "$CONF" ]; then
  tmp2=$(mktemp)
  trap 'rm -f "${tmp:-}" "$tmp2"' EXIT
  sed "s/^ECONUT_SHUTDOWN_BELOW=.*/ECONUT_SHUTDOWN_BELOW=${TRIGGER:-55}/" "$HERE/econut.conf" >"$tmp2"
  install_file "$tmp2" "$CONF" 0644
else
  BACKED_UP=0
  # ECONUT_TRIGGER_BELOW was the earlier name of ECONUT_SHUTDOWN_BELOW.
  if grep -q '^ECONUT_TRIGGER_BELOW=' "$CONF"; then
    run cp -p "$CONF" "$CONF.bak.$STAMP"; BACKED_UP=1
    run sed -i 's/^ECONUT_TRIGGER_BELOW=/ECONUT_SHUTDOWN_BELOW=/' "$CONF"
    say "renamed ECONUT_TRIGGER_BELOW to ECONUT_SHUTDOWN_BELOW in $CONF"
  fi
  if [ "$TRIGGER_SET" = 1 ]; then
    [ "$BACKED_UP" = 1 ] || run cp -p "$CONF" "$CONF.bak.$STAMP"
    run sed -i "s/^ECONUT_SHUTDOWN_BELOW=.*/ECONUT_SHUTDOWN_BELOW=$TRIGGER/" "$CONF"
    say "set ECONUT_SHUTDOWN_BELOW=$TRIGGER in $CONF"
  else
    say "kept your existing $CONF (pass --shutdown-below N to change the level)"
  fi
fi

# ---- 4. binaries + unit -----------------------------------------------------
say "binaries and systemd unit"
install_file "$ROOT/bin/econut-guard" /usr/local/bin/econut-guard 0755 || true
install_file "$ROOT/bin/econut" /usr/local/bin/econut 0755 || true
if install_file "$HERE/econut-guard.service" /etc/systemd/system/econut-guard.service 0644; then
  run systemctl daemon-reload
fi
if [ "$ENABLE" = 1 ]; then
  run systemctl enable econut-guard.service
  run systemctl restart econut-guard.service # enable --now would leave an already-running old process
  say "econut-guard enabled and ARMED (will power the machine off)"
elif [ "$DRY" = 0 ] && systemctl is-active --quiet econut-guard.service; then
  run systemctl restart econut-guard.service # pick up a new binary or level
  say "restarted the running econut-guard"
else
  say "econut-guard installed but not enabled (use --enable-service to arm it)"
fi

# ---- 5. verify --------------------------------------------------------------
if [ "$DRY" = 0 ]; then
  say "checking the UPS through NUT"
  sleep 3
  if /usr/local/bin/econut-guard --once; then
    say "OK: econut-guard can read the EcoFlow through NUT"
  else
    die "econut-guard --once failed; check: systemctl status nut-server nut-driver@$UPS_NAME; upsc $UPS_NAME@localhost"
  fi
  if systemctl is-active --quiet econut-guard.service; then
    sleep 6
    /usr/local/bin/econut status || true
  fi
fi

cat <<EOF

Done. When running, econut-guard WILL power the machine off if the condition is met.
Check on it any time with:  econut status
Watch what the UPS reports (every value change is logged):  journalctl -u econut-guard -f
EOF

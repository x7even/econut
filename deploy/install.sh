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
# NUT's own upsmon is configured as a last-resort backstop below that level,
# for the case where the econut-guard process is gone while NUT still works.
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
    -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
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

# Some of the temporaries below hold the NUT monitor password, so collect them
# all in one place and clear them on any exit.
TMPFILES=()
cleanup() { [ ${#TMPFILES[@]} -eq 0 ] || rm -f "${TMPFILES[@]}"; }
trap cleanup EXIT
# Dying on a signal - including the SIGPIPE from piping this into head - would
# otherwise skip the EXIT trap and leave a password behind in /tmp.
trap 'exit 1' INT TERM HUP PIPE
# mktempf VARNAME -- make a temporary file, remember it for cleanup, and put its
# path in VARNAME. It assigns rather than prints because a $(...) call would run
# in a subshell, where appending to TMPFILES would be thrown away.
mktempf() {
  local f
  f=$(mktemp) || die "cannot create a temporary file"
  TMPFILES+=("$f")
  printf -v "$1" '%s' "$f"
}

# monitor_password FILE -- the password from the [monitor] section of upsd.users.
# A re-run keeps whatever password is already installed, so the MONITOR line in
# upsmon.conf has to be built from the live file rather than from a fresh one.
monitor_password() {
  [ -r "$1" ] || return 1
  local pw
  pw=$(awk '
    /^[[:space:]]*\[/ { inmon = ($0 ~ /^[[:space:]]*\[monitor\][[:space:]]*$/); next }
    inmon && /^[[:space:]]*password[[:space:]]*=/ {
      sub(/^[[:space:]]*password[[:space:]]*=[[:space:]]*/, "")
      sub(/[[:space:]]+$/, "")
      print; exit
    }
  ' "$1")
  # An empty result has to fail: a MONITOR line with no password is precisely
  # the broken config that leaves nut-monitor dead.
  [ -n "$pw" ] || return 1
  printf '%s\n' "$pw"
}

# fill_placeholder SRC PW -- SRC with the password placeholder replaced, by
# literal substring so nothing in the password is read as a metacharacter.
# has_primary_role FILE / downgrade_primary_role FILE -- the 'upsmon primary'
# grant that earlier versions wrote into upsd.users. These only ever run against
# a root-owned file, so they are functions purely to make them testable; the
# password line must survive byte for byte.
has_primary_role() {
  grep -q '^[[:space:]]*upsmon[[:space:]]\+primary[[:space:]]*$' "$1"
}

downgrade_primary_role() {
  sed -i 's/^\([[:space:]]*\)upsmon[[:space:]]\+primary[[:space:]]*$/\1upsmon secondary/' "$1"
}

fill_placeholder() {
  # The password goes through the environment, not -v: awk expands backslash
  # escapes in a -v assignment, which would corrupt a password containing one.
  econut_pw=$2 awk '
    { ph = "CHANGE-ME-before-installing"
      i = index($0, ph)
      if (i) $0 = substr($0, 1, i - 1) ENVIRON["econut_pw"] substr($0, i + length(ph))
      print }
  ' "$1"
}

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
# The driver directory moves between distributions and NUT versions
# (/usr/libexec/nut since 2.8, /lib/nut and /usr/lib/nut before it), so look in
# all of them rather than deciding NUT is missing because one path is absent.
driver_found=0
for d in /usr/libexec/nut /lib/nut /usr/lib/nut /usr/local/libexec/nut /usr/local/lib/nut; do
  if [ -x "$d/usbhid-ups" ]; then
    driver_found=1
    say "found the NUT usbhid-ups driver in $d"
    break
  fi
done
[ "$driver_found" = 1 ] || command -v usbhid-ups >/dev/null ||
  die "cannot find NUT's usbhid-ups driver; on Debian/Ubuntu: sudo apt install nut-server"
getent group nut >/dev/null || die "group 'nut' missing; is the nut-server package installed?"

remove_old_names

# ---- 1. udev rule -----------------------------------------------------------
say "udev rule (lets group nut open the EcoFlow USB device)"
if install_file "$HERE/99-ecoflow-ups.rules" /etc/udev/rules.d/99-ecoflow-ups.rules 0644; then
  run udevadm control --reload
  run udevadm trigger --subsystem-match=usb --attr-match=idVendor=3746
  # The usb trigger does not re-run rules for the serial child device, so the
  # ModemManager tag would not land on the tty node until a replug or a reboot.
  run udevadm trigger --action=change --subsystem-match=tty
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
  # Earlier versions granted the monitor user the 'primary' upsmon role, which
  # authorises the POWERDOWNFLAG path described in upsmon.conf. econut never
  # wants it, so take it away from installs that already have it.
  if has_primary_role /etc/nut/upsd.users; then
    run cp -p /etc/nut/upsd.users "/etc/nut/upsd.users.bak.$STAMP"
    run downgrade_primary_role /etc/nut/upsd.users
    say "downgraded the monitor user from upsmon primary to secondary in /etc/nut/upsd.users"
    changed=1
  else
    say "unchanged: /etc/nut/upsd.users (monitor user already present)"
  fi
else
  mktempf tmp
  pw=$(head -c 24 /dev/urandom | base64 | tr -d '/+=\n')
  sed "s/CHANGE-ME-before-installing/$pw/" "$HERE/upsd.users" >"$tmp"
  install_file "$tmp" /etc/nut/upsd.users 0640 root:nut && changed=1
fi

# MODE=standalone means the distro also runs upsmon (nut-monitor.service).
# The packaged upsmon.conf has no MONITOR line, so upsmon exits straight away
# and the unit sits in 'failed' -- a permanently red systemctl and, more to the
# point, no backstop at all if econut-guard stops working. Give it a real
# config, with the password that is actually in upsd.users.
say "upsmon backstop (/etc/nut/upsmon.conf)"
if monpw=$(monitor_password /etc/nut/upsd.users); then
  mktempf tmpmon
  fill_placeholder "$HERE/upsmon.conf" "$monpw" >"$tmpmon"
  install_file "$tmpmon" /etc/nut/upsmon.conf 0640 root:nut && changed=1
elif [ "$DRY" = 1 ]; then
  say "[dry-run] /etc/nut/upsd.users is not readable as $(id -un); would install"
  say "[dry-run] /etc/nut/upsmon.conf with the monitor password taken from it"
else
  die "no monitor password found in /etc/nut/upsd.users, so the MONITOR line in
    upsmon.conf would not work; check the [monitor] section of that file"
fi

if [ "$changed" = 1 ]; then
  say "restarting NUT"
  run systemctl restart nut-driver-enumerator.service 2>/dev/null ||
    say "note: nut-driver-enumerator not present, continuing"
  run systemctl restart "nut-driver@$UPS_NAME.service" 2>/dev/null || true
  run systemctl restart nut-server.service
  # upsmon needs upsd up before it can log in, so it goes last. A failure here
  # is reported by the backstop check in step 5, which says more than
  # systemctl's exit code would.
  run systemctl reset-failed nut-monitor.service 2>/dev/null || true
  run systemctl restart nut-monitor.service 2>/dev/null || true
fi

# ---- 3. econut config -------------------------------------------------------
say "econut config ($CONF)"
run install -d -m 0755 /etc/econut
if [ ! -f "$CONF" ]; then
  mktempf tmp2
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
  say "econut-guard enabled; confirm it can see the UPS with: econut check"
elif systemctl is-active --quiet econut-guard.service; then
  run systemctl restart econut-guard.service # pick up a new binary or level
  [ "$DRY" = 1 ] || say "restarted the running econut-guard"
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
  # Tier 2: upsmon. Being up is not enough -- given a password upsd rejects it
  # stays up and retries the login indefinitely, so is-active would say yes
  # while nothing was watching. upsd's client list is the evidence: econut-guard
  # only ever sends LIST VAR and GET VAR, never LOGIN, so upsmon is the only
  # thing here that appears in it.
  say "checking the low-battery backstop"
  if ! systemctl is-active --quiet nut-monitor.service; then
    say "nut-monitor is not running; clearing the failed state and starting it"
    run systemctl reset-failed nut-monitor.service 2>/dev/null || true
    run systemctl restart nut-monitor.service 2>/dev/null || true
  fi
  # upsmon that is going to exit can still look active for a moment, and the
  # login takes a poll, so give it time before believing either signal.
  sleep 5
  if ! systemctl is-active --quiet nut-monitor.service; then
    die "nut-monitor is not running, so there is no low-battery backstop:
    systemctl status nut-monitor; journalctl -u nut-monitor -n 20"
  fi
  if [ -n "$(upsc -c "$UPS_NAME@localhost" 2>/dev/null)" ]; then
    say "OK: upsmon is logged in to upsd and will shut down on low battery"
  else
    die "nut-monitor is running but never logged in to upsd, so the backstop
    would not act. This is usually a password mismatch between the MONITOR line
    in /etc/nut/upsmon.conf and the [monitor] user in /etc/nut/upsd.users:
    journalctl -u nut-monitor -n 20"
  fi

  # econut-guard is meant to act well before the UPS calls low battery. If the
  # level has been set at or under the UPS's own threshold, upsmon gets there
  # first and the configured level never does anything.
  low=$(upsc "$UPS_NAME@localhost" battery.charge.low 2>/dev/null || true)
  lvl=$(sed -n 's/^ECONUT_SHUTDOWN_BELOW=//p' "$CONF" 2>/dev/null | head -1)
  if [ -n "$low" ] && [ -n "$lvl" ] &&
     awk -v a="$lvl" -v b="$low" 'BEGIN { exit !(a <= b) }'; then
    say "warning: ECONUT_SHUTDOWN_BELOW=$lvl is at or below the UPS low-battery
    level ($low%), so the upsmon backstop would fire first and the level you set
    would never trigger; raise it with: sudo econut set --shutdown-below N"
  fi
  if systemctl is-active --quiet econut-guard.service; then
    sleep 6
    /usr/local/bin/econut status || true
  fi
fi

cat <<EOF

Done. When running, econut-guard WILL power the machine off if the condition is met.
Check on it any time with:  econut status
Confirm it is actually protecting the machine:  econut check
Watch what the UPS reports (every value change is logged):  journalctl -u econut-guard -f
EOF

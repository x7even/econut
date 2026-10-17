#!/usr/bin/env bash
# Exercise install.sh's monitor_password / fill_placeholder against the real
# shipped code, which a non-root --dry-run never reaches.
set -uo pipefail
SH=$1
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

sed -n '/^monitor_password()/,/^}/p' "$SH" >"$TMP/fns.sh"
sed -n '/^fill_placeholder()/,/^}/p' "$SH" >>"$TMP/fns.sh"
grep -c . "$TMP/fns.sh" >/dev/null || { echo "FAIL: could not extract functions"; exit 1; }
# shellcheck source=/dev/null
. "$TMP/fns.sh"

fails=0
ok()   { printf 'ok   %s\n' "$1"; }
bad()  { printf 'FAIL %s: %s\n' "$1" "$2"; fails=$((fails + 1)); }

# 1. ordinary password
cat >"$TMP/u1" <<'EOF'
[monitor]
  password = Abc123xyz
  upsmon secondary
EOF
got=$(monitor_password "$TMP/u1")
[ "$got" = "Abc123xyz" ] && ok "plain password" || bad "plain password" "got '$got'"

# 2. password full of shell/sed/awk metacharacters
cat >"$TMP/u2" <<'EOF'
[monitor]
  password = a&b/c\d$e"f'g
  upsmon secondary
EOF
got=$(monitor_password "$TMP/u2")
[ "$got" = 'a&b/c\d$e"f'"'"'g' ] && ok "metacharacter password" || bad "metacharacter password" "got '$got'"

# the placeholder must be replaced literally, with nothing re-interpreted
printf 'MONITOR ups@localhost 1 monitor CHANGE-ME-before-installing secondary\n' >"$TMP/tpl"
out=$(fill_placeholder "$TMP/tpl" "$got")
want='MONITOR ups@localhost 1 monitor a&b/c\d$e"f'"'"'g secondary'
[ "$out" = "$want" ] && ok "literal substitution" || bad "literal substitution" "got '$out'"

# 3. a password in another section must not be picked up
cat >"$TMP/u3" <<'EOF'
[admin]
  password = WRONG-ONE
  actions = SET
[monitor]
  password = RightOne
  upsmon secondary
EOF
got=$(monitor_password "$TMP/u3")
[ "$got" = "RightOne" ] && ok "picks the [monitor] section" || bad "picks the [monitor] section" "got '$got'"

# 4. [monitor] after another section's password, no monitor password -> fail
cat >"$TMP/u4" <<'EOF'
[admin]
  password = WRONG-ONE
[monitor]
  upsmon secondary
EOF
if monitor_password "$TMP/u4" >/dev/null; then
  bad "no monitor password" "returned success"
else
  ok "no monitor password returns non-zero"
fi

# 5. unreadable file -> fail, not a crash
if monitor_password "$TMP/does-not-exist" >/dev/null 2>&1; then
  bad "missing file" "returned success"
else
  ok "missing file returns non-zero"
fi

# 6. the real shipped upsmon.conf renders to exactly one usable MONITOR line
real=$(dirname "$SH")/upsmon.conf
rendered=$(fill_placeholder "$real" "Secret123")
n=$(printf '%s\n' "$rendered" | grep -c '^MONITOR ')
[ "$n" = 1 ] && ok "one MONITOR line" || bad "one MONITOR line" "found $n"
line=$(printf '%s\n' "$rendered" | grep '^MONITOR ')
[ "$line" = "MONITOR ecoflow@localhost 1 monitor Secret123 secondary" ] &&
  ok "MONITOR line is correct and secondary" || bad "MONITOR line" "got '$line'"
printf '%s\n' "$rendered" | grep -q 'CHANGE-ME-before-installing' &&
  bad "placeholder left behind" "still present" || ok "no placeholder left"

[ "$fails" = 0 ] && { echo "ALL PASS"; exit 0; } || { echo "$fails FAILED"; exit 1; }

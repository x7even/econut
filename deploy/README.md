# Deploying econut

Without a checkout: `go install github.com/x7even/econut/cmd/...@latest`, then
`sudo ~/go/bin/econut install [installer options]`. This unpacks the embedded copy of this
directory to a temporary folder and runs the installer below with the same options.

From a checkout, build, then run the installer as root:

```sh
go build -o bin/ ./cmd/...
sudo deploy/install.sh --dry-run     # print what it would do (run as root to see real diffs)
sudo deploy/install.sh               # install with the default level (55%)
sudo deploy/install.sh --shutdown-below 30   # or pick your own level
```

The installer:

1. installs the udev rule (`99-ecoflow-ups.rules`) so group `nut` can open the EcoFlow (`3746:ffff`);
2. installs `nut.conf`, `ups.conf`, `upsd.conf` and `upsd.users` into `/etc/nut` (mode 0640, group `nut`), generating a random password for `upsd.users`, and restarts NUT;
3. writes `/etc/econut/econut.conf` (the shutdown level and debounce). An existing file is kept unless you pass `--shutdown-below`;
4. installs `/usr/local/bin/econut-guard`, `/usr/local/bin/econut` and the systemd unit (enabled only with `--enable-service`; a running guard is restarted);
5. runs `econut-guard --once` to prove it can read the battery through NUT.

Existing files that differ are backed up as `<file>.bak.<timestamp>`; identical files are left alone, so re-running is safe. A leftover `ups-guard` install (the old name) is removed.

Options: `--shutdown-below N` sets the level; `--enable-service` also enables and starts the unit (arming the real shutdown); `--uninstall` removes the unit, binaries and udev rule (it leaves `/etc/nut` and `/etc/econut` alone).

Requires the NUT package (`sudo apt install nut-server` on Debian/Ubuntu).

## Changing the level later

```sh
sudo econut set --shutdown-below 40            # shut down below 40% (on battery)
sudo econut set --debounce 90s                # ...held for 90 s
sudo econut set --shutdown-below 40 --debounce 90s
```

`econut set` validates the values, saves them to `/etc/econut/econut.conf` and restarts the guard (`--no-restart` only saves). `econut status` shows the level in effect. Editing `ECONUT_SHUTDOWN_BELOW` / `ECONUT_DEBOUNCE` in that file by hand and restarting the service works too.

## Checking on it

```
$ econut status
● econut-guard: running, ARMED (will power the machine off)

  UPS        ecoflow@127.0.0.1:3493
  Power      on mains (OL CHRG)
  Battery    ████████████████████ 99%
  Shutdown   when on battery and below 55% for 120s
  Condition  not met (on mains)
```

Colours appear on a terminal; they are off when piped, with `NO_COLOR` set, or `TERM=dumb`.

A guard that is running is not necessarily protecting the machine. When the UPS link or the NUT driver dies, every reading becomes unusable and a low battery would go unnoticed:

```
$ econut status
● econut-guard: running, NOT PROTECTED (no usable UPS reading for 84h0m0s; a low battery would not be noticed)

  UPS        <ups>
  Power      no reading for 84h0m0s (nut: <ups> ups.status: DRIVER-NOT-CONNECTED)
  Battery    unknown
  Shutdown   when on battery and below 55% for 120s
  Condition  UNKNOWN - nothing is watching the battery
```

`econut status --json` prints the raw snapshot, plus `protection` and `exit_code`. Both commands exit with the verdict:

| code | meaning |
| --- | --- |
| 0 | protected: running, live, last reading usable. The only zero exit. |
| 1 | not running: no status file, unreadable, unparsable, not fresh, or dated in the future. |
| 2 | usage error. |
| 3 | running but unprotected through a fault: no usable reading, a `ups.status` that says neither `OB` nor `OL`, a poweroff attempt that failed, or an unrecognised mode. |
| 4 | running and reading the UPS, but in dry-run: unprotected by configuration. |

Codes 0, 1 and 2 keep the meanings they have always had, so `econut status || true` and `-eq 1` checks are unaffected. A blind or dry-run guard, which used to exit 0, now exits 3 or 4.

A script that acts on *any* non-zero exit needs a second look, because 3 and 4 are not "the guard is gone": restarting `econut-guard` on a non-zero exit would restart it every five minutes for as long as the UPS link is down, which is exactly when the journal needs to be readable. Gate a restart on exit 1 alone, and treat 3 and 4 as something to report.

The faults behind exit 3 are:

| headline | what it means |
| --- | --- |
| `no usable UPS reading` | the reads are failing, or no read has ever succeeded. `blind_seconds` and `ever_valid` in the JSON say for how long and whether one ever worked. |
| `it asked the machine to power off and that failed` | the UPS is fine; `systemctl poweroff` is not. The guard retries every poll, and `shutdown_failed` in the JSON carries the error. |
| `says neither OB nor OL` | the read worked but `ups.status` cannot be interpreted - econut decides on the `OB` token alone, so such a reading says nothing about the power source and is never read as mains. |
| `unknown mode` | the snapshot was written by a build this `econut` does not understand. |

### Reading the status file directly

The guard writes `/run/econut/status.json` at mode 0644 every poll. It holds **measurements, not a verdict**: `valid`, `ups_status`, `charge`, `on_battery`, `held_seconds`, `triggered`, `blind_seconds`, `ever_valid`, `stale_after_seconds`, `shutdown_failed`, `mode`, `poll_seconds`, `shutdown_below`, `debounce_seconds` and `updated`.

Whether the machine is protected depends on when you ask - a snapshot that was protected when written is "not running" fifteen seconds later - so the verdict is computed by the reader and deliberately not stored. `econut status --json` adds the `protection` and `exit_code` it computed at that moment; a `protection` field in the file itself would be a fact with an expiry date in a file that outlives the guard.

If you consume the file rather than the command, **test `valid` first**: `charge` and `on_battery` are the zero values of a reading that failed, so `{"valid":false,"charge":0,"on_battery":false}` means "no reading", not "on mains at 0%". Likewise a non-empty `shutdown_failed` means the machine could not be powered off, and `blind_seconds` is only meaningful when `stale_after_seconds` is present - a snapshot from a guard that predates these keys has both at zero because it recorded neither, not because it had just read the UPS.

### Gating a long job

```sh
econut check || exit 1            # refuse to start unless the machine is protected
econut check --max-age 10s        # also require a snapshot newer than 10 s
```

`econut check` prints one line and exits with the same code as `econut status`; `--quiet` suppresses the line, and `--allow-dry-run` accepts a dry-run guard that can still see the UPS. `--max-age` is the only thing it adds to the verdict, and it only ever tightens it: the snapshot's own freshness window is three poll intervals (15 s at the default poll), so a larger `--max-age` changes nothing and a negative one is rejected rather than silently ignored. No fault is laundered by `--allow-dry-run`: a dry-run guard that cannot see the UPS, or whose poweroff fails, still exits 3. There is no grace period: one failed read is enough to report NOT PROTECTED, which fails in the safe direction.

### Being told about it

To be notified rather than only accrue journal lines, run the check on a timer and hang a handler off it:

```ini
# /etc/systemd/system/econut-check.service
[Unit]
OnFailure=econut-unprotected.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/econut check

# /etc/systemd/system/econut-check.timer
[Timer]
OnBootSec=2min
OnUnitActiveSec=5min

[Install]
WantedBy=timers.target
```

Then `sudo systemctl daemon-reload && sudo systemctl enable --now econut-check.timer`.

`econut-unprotected.service` is yours to write: send mail, post to a webhook, light an LED. The guard itself never execs anything on your behalf, so nothing can block or delay a shutdown decision.

Note that `--stale-after` sets both the escalation threshold and the longest gap the debounce will bridge, so raising it to quiet the errors also widens the gap the guard will treat as continuous. Both are measured from the last usable reading. A run of failed reads that a reading bridges before any single poll crossed `--stale-after` is logged once when the reading returns (`UPS readings were unusable for longer than stale-after`), since an outage inside such a gap would not have powered the machine off.

## The shutdown is live

The service runs as root and executes `systemctl poweroff` when the machine is on battery and below the level for the debounce period. To only log instead (for testing), add `--dry-run` to `ExecStart` in `/etc/systemd/system/econut-guard.service`, then `sudo systemctl daemon-reload && sudo systemctl restart econut-guard`.

## Watching a mains event

`journalctl -u econut-guard -f` logs a `UPS values changed` line whenever any value NUT reports changes (status, charge, input voltage, alarms, ...; `battery.runtime` is excluded as noise), for example `ups.status="OL CHRG -> OB DISCHRG"`.

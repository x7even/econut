# Deploying econut

Build, then run the installer as root:

```sh
go build -o bin/ ./cmd/...
sudo deploy/install.sh --dry-run     # print what it would do (run as root to see real diffs)
sudo deploy/install.sh               # install with the default level (55%)
sudo deploy/install.sh --trigger-below 30   # or pick your own level
```

The installer:

1. installs the udev rule (`99-ecoflow-ups.rules`) so group `nut` can open the EcoFlow (`3746:ffff`);
2. installs `nut.conf`, `ups.conf`, `upsd.conf` and `upsd.users` into `/etc/nut` (mode 0640, group `nut`), generating a random password for `upsd.users`, and restarts NUT;
3. writes `/etc/econut/econut.conf` (the trigger level and debounce). An existing file is kept unless you pass `--trigger-below`;
4. installs `/usr/local/bin/econut-guard`, `/usr/local/bin/econut` and the systemd unit (enabled only with `--enable-service`; a running guard is restarted);
5. runs `econut-guard --once` to prove it can read the battery through NUT.

Existing files that differ are backed up as `<file>.bak.<timestamp>`; identical files are left alone, so re-running is safe. A leftover `ups-guard` install (the old name) is removed.

Options: `--trigger-below N` sets the level; `--enable-service` also enables and starts the unit (arming the real shutdown); `--uninstall` removes the unit, binaries and udev rule (it leaves `/etc/nut` and `/etc/econut` alone).

Requires the NUT package (`sudo apt install nut-server` on Debian/Ubuntu).

## Changing the level later

```sh
sudo econut set --trigger-below 40            # shut down below 40% (on battery)
sudo econut set --debounce 90s                # ...held for 90 s
sudo econut set --trigger-below 40 --debounce 90s
```

`econut set` validates the values, saves them to `/etc/econut/econut.conf` and restarts the guard (`--no-restart` only saves). `econut status` shows the level in effect. Editing `ECONUT_TRIGGER_BELOW` / `ECONUT_DEBOUNCE` in that file by hand and restarting the service works too.

## Checking on it

```
$ econut status
econut-guard: running, ARMED (will power the machine off)
UPS ecoflow@127.0.0.1:3493: on mains (OL CHRG), battery 99%
Shuts down when: on battery and below 55% for 120s
Condition: not met (on mains)
```

`econut status --json` prints the raw snapshot. The exit status is 0 when the guard is running and 1 when it isn't, so it works in scripts.

## The shutdown is live

The service runs as root and executes `systemctl poweroff` when the machine is on battery and below the level for the debounce period. To only log instead (for testing), add `--dry-run` to `ExecStart` in `/etc/systemd/system/econut-guard.service`, then `sudo systemctl daemon-reload && sudo systemctl restart econut-guard`.

## Watching a mains event

`journalctl -u econut-guard -f` logs a `UPS values changed` line whenever any value NUT reports changes (status, charge, input voltage, alarms, ...; `battery.runtime` is excluded as noise), for example `ups.status="OL CHRG -> OB DISCHRG"`.

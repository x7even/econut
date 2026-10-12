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
4. installs `/usr/local/bin/econut-guard`, `/usr/local/bin/econut` and the systemd unit, **not enabled**;
5. runs `econut-guard --once` to prove it can read the battery through NUT.

Existing files that differ are backed up as `<file>.bak.<timestamp>`; identical files are left alone, so re-running is safe. A leftover `ups-guard` install (the old name) is removed.

Options: `--trigger-below N` sets the level; `--enable-service` also enables the unit (still dry-run, log only); `--uninstall` removes the unit, binaries and udev rule (it leaves `/etc/nut` and `/etc/econut` alone).

Requires the NUT package (`sudo apt install nut-server` on Debian/Ubuntu).

## Changing the level later

Edit `ECONUT_TRIGGER_BELOW` (and `ECONUT_DEBOUNCE`) in `/etc/econut/econut.conf`, then `sudo systemctl restart econut-guard`. `econut status` shows the level in effect.

## Checking on it

```
$ econut status
econut-guard: running, dry-run (will NOT shut down)
UPS ecoflow@127.0.0.1:3493: on mains (OL CHRG), battery 99%
Shuts down when: on battery and below 55% for 120s
Condition: not met (on mains)
```

`econut status --json` prints the raw snapshot. The exit status is 0 when the guard is running and 1 when it isn't, so it works in scripts.

## Enabling real shutdown (deliberately manual)

The installer never does this. Run the dry-run service through a real outage first and check `journalctl -u econut-guard` shows the `OB` status and a sensible `WOULD shut down` line. Then add `--enable-shutdown` to `ExecStart` in `/etc/systemd/system/econut-guard.service`, replace the `DynamicUser=yes` line with `User=root` (or a user permitted to run `systemctl poweroff`), and `sudo systemctl daemon-reload && sudo systemctl restart econut-guard`.

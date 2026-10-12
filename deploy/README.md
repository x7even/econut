# Deploying ups-guard

Build, then run the installer as root:

```sh
go build -o bin/ups-guard ./cmd/ups-guard
sudo deploy/install.sh --dry-run     # print what it would do (run as root to see real diffs)
sudo deploy/install.sh               # install
```

The installer:

1. installs the udev rule (`99-ecoflow-ups.rules`) so group `nut` can open the EcoFlow (`3746:ffff`);
2. installs `nut.conf`, `ups.conf`, `upsd.conf` and `upsd.users` into `/etc/nut` (mode 0640, group `nut`), generating a random password for `upsd.users`, and restarts NUT. Existing files that differ are backed up as `<file>.bak.<timestamp>`; identical files are left alone, so re-running is safe;
3. installs `/usr/local/bin/ups-guard` and the systemd unit, **not enabled**;
4. runs `ups-guard --once` to prove it can read the battery through NUT.

Options: `--enable-service` also enables the unit (still dry-run, log only); `--uninstall` removes the unit, binary and udev rule (it leaves `/etc/nut` alone).

Requires the NUT package (`sudo apt install nut-server` on Debian/Ubuntu).

## Enabling real shutdown (deliberately manual)

The installer never does this. Run the dry-run service through a real outage first and check `journalctl -u ups-guard` shows the `OB` status and a sensible `WOULD shut down` line. Then add `--enable-shutdown` to `ExecStart` in `/etc/systemd/system/ups-guard.service`, change `User=` to root (or a user permitted to run `systemctl poweroff`), and `sudo systemctl daemon-reload && sudo systemctl restart ups-guard`.

# Deploying ups-guard (manual, needs sudo; review each step)

Nothing here is installed automatically.

1. Build: `go build -o bin/ups-guard ./cmd/ups-guard`
2. udev: `sudo cp deploy/99-ecoflow-ups.rules /etc/udev/rules.d/ && sudo udevadm control --reload && sudo udevadm trigger`
3. NUT: copy `nut.conf`, `ups.conf`, `upsd.conf`, `upsd.users` to `/etc/nut/` (set a real password in `upsd.users` first; `chmod 640`, group `nut`). Then `sudo systemctl restart nut-driver-enumerator nut-server`.
4. Check: `upsc ecoflow@localhost` shows `battery.charge` and `ups.status`.
5. Binary: `sudo install bin/ups-guard /usr/local/bin/`
6. Service: `sudo cp deploy/ups-guard.service /etc/systemd/system/` then `sudo systemctl daemon-reload`. Do not enable it until you have run dry-run through a real outage.
7. Enabling real shutdown: add `--enable-shutdown` to `ExecStart` and change `User=` to a user permitted to run `systemctl poweroff` (or use root). Do this only after reviewing the dry-run logs.

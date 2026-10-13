# econut

Safe shutdown for a Linux machine powered through an **EcoFlow Delta 3 Ultra Plus** or other EcoFlow UPS NUT/HID Supported Backup Battery.

When mains power fails and the EcoFlow's battery runs down to a level you choose, `econut-guard`
shuts your machine down cleanly, before the battery runs out and cuts power hard. If mains comes
back first, nothing happens.

By default: **on battery and below 55% for 120 seconds → `systemctl poweroff`.** Both numbers are
yours to change.

```
$ econut status
● econut-guard: running, ARMED (will power the machine off)

  UPS        ecoflow@127.0.0.1:3493
  Power      on mains (OL CHRG)
  Battery    ████████████████████ 99%
  Shutdown   when on battery and below 55% for 120s
  Condition  not met (on mains)
```

> **First iteration, not a release.** Mains-loss detection has been verified on one unit; a full
> battery-to-shutdown run has not. Once the guard is running it **will power your machine off**
> when the condition is met. Use `--dry-run` (below) if you want to watch first.

## How it works

The EcoFlow's USB-A port speaks USB HID Power Device. [NUT](https://networkupstools.org/) reads
it, and `econut-guard` polls NUT every 5 seconds. The EcoFlow reports battery percentage and
mains status (`OL` on mains, `OB` on battery) over USB. It does not report watts, load or
temperature over USB.
*Note if you're struggling to connect with the USB port on the Ecoflow marked 'HID' and using a USB-C to USB-C cable, change to USB-C - A or use an A adapter to force data / block self negotiation.

## Requirements

- Linux with systemd, and root access.
- The EcoFlow's **USB-A** port connected to the machine you want to protect (a USB-C to USB-C
  cable did not work on the unit we tested).
- NUT: `sudo apt install nut-server` (Debian/Ubuntu).
- Go 1.22 or newer, to build.

## Install

No clone needed. Two commands:

```sh
go install github.com/x7even/econut/cmd/...@latest
sudo ~/go/bin/econut install --enable-service
```

(`~/go/bin` is where `go install` puts the binaries; use `$(go env GOBIN)` or `$(go env GOPATH)/bin`
if you have changed that. The full path matters because `sudo` often does not have it on `PATH`.)

To pick your own shutdown level at install time, add `--shutdown-below 40`.

The deployment files are built into the `econut` binary. `econut install` sets up the udev rule,
the NUT configuration, the `econut-guard` service and copies both commands to `/usr/local/bin`. It
is safe to re-run: files that already match are left alone and changed ones are backed up.
`sudo ~/go/bin/econut install --dry-run` shows what it would do, and `--uninstall` removes it.

Prefer a checkout? `git clone https://github.com/x7even/econut.git && cd econut && go build -o bin/ ./cmd/... && sudo deploy/install.sh --enable-service` does the same thing.

## Check it works

```sh
econut status                    # is the guard running, and what is the battery at?
journalctl -u econut-guard -f    # watch it live
```

Unplug the EcoFlow from mains for a few seconds and plug it back in. The journal shows what the
unit reports, for example:

```
UPS values changed  ups.status="OL -> OB DISCHRG"
UPS values changed  ups.status="OB DISCHRG -> OL DISCHRG"
UPS values changed  ups.status="OL DISCHRG -> OL CHRG"
```

## Change the settings

```sh
sudo econut set --shutdown-below 40                       # shut down below 40% battery
sudo econut set --debounce 90s                            # ...after 90 s continuously below it
sudo econut set --shutdown-below 40 --debounce 90s        # or both
```

This saves to `/etc/econut/econut.conf` and restarts the guard. `econut status` shows the
values in effect.

## Try it without shutting down

Add `--dry-run` to the `ExecStart=` line in `/etc/systemd/system/econut-guard.service`, then
`sudo systemctl daemon-reload && sudo systemctl restart econut-guard`. The guard logs
`WOULD shut down` instead of acting, and `econut status` says `dry-run`. Remove the flag to arm
it again.

## Troubleshooting

- `econut status` says NOT RUNNING: `systemctl status econut-guard` and
  `journalctl -u econut-guard -n 50`.
- No reading from the UPS: `upsc ecoflow@localhost` should list values; if it does not, check
  `systemctl status nut-server nut-driver@ecoflow` and that the cable is in the USB-A port.
- Colours off: set `NO_COLOR=1`, or pipe the output.

More detail is in [deploy/README.md](deploy/README.md); design notes are in `docs/`.

Go standard library only. `go test ./...` runs the suite, and no test can power anything off.

MIT licensed.

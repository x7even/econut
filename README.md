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

## Update

The same two commands, in the same order:

```sh
go install github.com/x7even/econut/cmd/...@latest
sudo ~/go/bin/econut install
```

Both are needed. `go install` only refreshes `~/go/bin`; it does not touch `/usr/local/bin`,
which is where the service runs from, and it does not restart anything. The second command is
what updates the machine: it backs up the binaries it replaces, restarts a running
`econut-guard` so the new one is actually live, and keeps your `/etc/econut/econut.conf`, so the
shutdown level you chose survives the update. Add `--enable-service` if the guard was never
enabled.

Then confirm the update took effect:

```sh
econut check                                             # exit 0 means protected right now
go version -m /usr/local/bin/econut-guard | grep -w mod  # the build the service runs
systemctl show econut-guard -p ExecMainStartTimestamp    # it should post-date the update
```

A release build names the module and its tag, as in `mod github.com/x7even/econut v0.3.0`. A
pseudo-version ending in `+dirty`, or a bare `mod econut`, is an uninstalled local build from
before the rename, which means the copy into `/usr/local/bin` never ran. Swap the path for
`"$(command -v econut)"` to ask the same of the CLI.

On-disk and running are separate questions, which is why the timestamp is there: it is the
daemon that protects the machine, and it only picks up a new binary when it restarts.

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

Both tiers of protection should check out too. Tier 1 is `econut-guard`, shutting down at the
level you chose; tier 2 is NUT's own `upsmon`, shutting down if the UPS itself reports low
battery (see [deploy/README.md](deploy/README.md#the-low-battery-backstop)):

```sh
econut check                     # tier 1: protected right now? exit 0 if so
systemctl is-active nut-monitor  # tier 2: expect active, not failed
upsc -c ecoflow@localhost        # tier 2: must list a client
udevadm info -n /dev/ttyACM0 | grep ID_MM   # expect ID_MM_DEVICE_IGNORE=1
```

`upsc -c` is the one that matters: it lists who is logged in to `upsd`, and `upsmon` appearing
there is the only proof its login succeeded. Given a password `upsd` rejects, `upsmon` stays
running and retries for ever, so `systemctl` showing it active does not mean it is watching the
battery. Expect one address, usually `127.0.0.1`; `upsc` also prints `Init SSL without
certificate database` on stderr, which is normal and is not a client. No address listed means
there is no tier 2 - re-run the installer.

## When it cannot see the UPS

A guard that is running is not necessarily protecting the machine. If the USB link or the NUT
driver dies, every reading becomes unusable and a low battery would go unnoticed, so `econut
status` says so plainly:

```
● econut-guard: running, NOT PROTECTED (no usable UPS reading for 84h0m0s; a low battery would not be noticed)

  UPS        <ups>
  Power      no reading for 84h0m0s (nut: <ups> ups.status: DRIVER-NOT-CONNECTED)
  Battery    unknown
  Shutdown   when on battery and below 55% for 120s
  Condition  UNKNOWN - nothing is watching the battery
```

The guard also logs a warning on the first failed read and escalates to an error once there
has been no usable reading for `--stale-after` (default 300 s), repeating once per interval
until a reading comes back. Blindness is measured from the last usable reading, not from the
first failed poll. A run of failures that a reading bridges before any single poll crossed
`--stale-after` is still reported when the reading returns, because a gap longer than
`--debounce` is longer than the debounce can bridge: an outage inside it would not have
powered the machine off.

Two more faults read as "running fine" if nobody checks for them, so they are part of the
verdict as well:

- **The poweroff did not work.** The guard asked the machine to shut down and the command
  failed. It retries on the next poll, and until one succeeds the status says
  `NOT PROTECTED (it asked the machine to power off and that failed: ...)` with a
  `last poweroff attempt FAILED` row. Telemetry can look perfect in this state.
- **`ups.status` cannot be interpreted.** econut decides on the `OB` token alone, so a status
  carrying neither `OB` nor `OL` - a best-fit subdriver that signals discharge some other
  way, or reports nothing at all - says nothing about the power source. Reading the missing
  `OB` as mains would be silent and wrong, and acting on it would power the machine off on
  healthy mains, so it is reported instead: `Power  unknown ("DISCHRG")`.

`econut status` exits with the verdict, so a script can act on it:

| code | meaning |
| --- | --- |
| 0 | protected: running, live, last reading usable. The only zero exit. |
| 1 | not running: no status file, unreadable, not fresh, or dated in the future. |
| 2 | usage error. |
| 3 | running but unprotected through a fault: no usable reading, a `ups.status` that says neither `OB` nor `OL`, a poweroff attempt that failed, or an unrecognised mode. |
| 4 | running and reading the UPS, but in dry-run: unprotected by configuration. |

For an unattended job, `econut check` is the one-line preflight:

```sh
econut check || exit 1            # refuse to start unless the machine is protected
econut check --max-age 10s        # also require a snapshot newer than 10 s
econut check --allow-dry-run      # accept a dry-run guard that can see the UPS
econut check --quiet              # exit code only
```

It reports the same verdict as `econut status`, from the same snapshot, and exits with the
same code - with the two differences it exists for: `--allow-dry-run` turns exit 4 into exit
0, and `--max-age` can turn any other verdict into exit 1. A `--max-age` longer than the
guard's own freshness window (three poll intervals) changes nothing, since it can only ever
make the answer stricter.

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

- `sudo: .../econut: command not found`: `go install` has not run, so `~/go/bin/econut` does
  not exist yet - installing is two commands and that is the first one. If you have changed
  `GOBIN`, use `$(go env GOBIN)/econut`, or `$(go env GOPATH)/bin/econut`, instead.
- An old version still running after `go install`: `go install` writes to `~/go/bin` and
  nothing else. The service runs `/usr/local/bin/econut-guard`, which the install step copies
  into place before restarting it, so `sudo ~/go/bin/econut install` is what makes a new build
  live. `go version -m /usr/local/bin/econut-guard | grep -w mod` says which build is on disk,
  and `systemctl show econut-guard -p ExecMainStartTimestamp` whether the daemon has restarted
  onto it.
- `nut-monitor` is `failed`, or `upsc -c <ups>` lists no client: tier 2 is down, so a low
  battery reported by the UPS itself would not shut the machine down. Re-run the installer
  (`sudo ~/go/bin/econut install`): it writes `upsmon.conf` with the password from `upsd.users`
  and then verifies the login. Tier 1 is unaffected, and `econut check` still speaks for it.
- `econut status` says NOT RUNNING: `systemctl status econut-guard` and
  `journalctl -u econut-guard -n 50`.
- `econut status` says NOT PROTECTED: the headline names which fault it is. Most often the
  guard is alive but cannot read the UPS; `journalctl -u econut-guard -n 50` has the driver's
  own error, and `upsc <ups>` shows whether NUT can read it at all. Until this clears,
  nothing will shut the machine down.
- NOT PROTECTED with `last poweroff attempt FAILED`: the UPS is fine and the shutdown command
  is not. The journal line `shutdown action failed` has the error; the guard runs
  `systemctl poweroff`, so check that it is running as root and that the command works when
  run by hand.
- NOT PROTECTED with `Power  unknown`: the read worked but `ups.status` says neither `OB` nor
  `OL`. `upsc <ups> ups.status` shows what the driver reports; a unit that signals discharge
  only through some other variable cannot be used by econut as it stands.
- No reading from the UPS: `upsc <ups>` should list values; if it does not, check
  `systemctl status nut-server nut-driver@ecoflow` and that the cable is in the USB-A port.
- NUT may bind a generic best-fit HID subdriver whose name does not match the hardware, so
  `ups.status` and `battery.charge` behaviour must be verified per unit with `upsc <ups>`
  rather than assumed. econut's decision reads only the `OB` token and `battery.charge`,
  never `LB` or `battery.charge.low`.
- Colours off: set `NO_COLOR=1`, or pipe the output.

More detail is in [deploy/README.md](deploy/README.md); design notes are in `docs/`.

Go standard library only. `go test ./...` runs the suite, and no test can power anything off.

MIT licensed.

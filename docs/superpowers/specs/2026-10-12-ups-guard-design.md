# ups-guard design

Date: 2026-10-12. Status: draft for user review.

## Purpose

The rig `9970x` runs behind an inverter and an EcoFlow Delta 3 Ultra Plus. If mains
fails and the EcoFlow battery drops below 55%, the rig must shut down cleanly.
`ups-guard` is a small Go daemon that makes that decision and, only when explicitly
enabled, performs the shutdown.

Out of scope: Prometheus metrics (parked by the user), the EcoFlow serial/cloud
APIs, controlling the EcoFlow.

## Success criteria

- Reads battery state from NUT every few seconds.
- Triggers once when the rig is on battery and charge is below 55% continuously for 120 s.
- **Never shuts down unless `--enable-shutdown` is passed.** Default is dry-run (log only).
- The policy is a pure function and fully unit-tested; no test can call `shutdown`.
- A NUT outage never causes a spurious shutdown and is logged loudly.

## Verified hardware facts (2026-10-12)

- EcoFlow `3746:ffff` exposes a HID Power Device interface plus a CDC-ACM serial port.
- The Linux kernel rejects its HID report descriptor (truncated, unbalanced collections),
  so there is no hidraw node. NUT's `usbhid-ups` parses it fine.
- No built-in NUT subdriver matches vendor 3746. Use
  `-x vendorid=3746 -x productid=ffff -x subdriver="APC HID"`.
- Observed while on mains: `battery.charge: 100`, `battery.runtime: 11848`,
  `ups.status: OL`.
- **Untested:** the `OB` (on battery) status during a real outage.

## Architecture

```
EcoFlow --USB HID--> usbhid-ups --> upsd (127.0.0.1:3493) --> ups-guard --> [dry-run log | poweroff]
```

Module `ups-guard`, Go standard library only.

| Package | Responsibility | Depends on |
|---|---|---|
| `internal/nut` | NUT text-protocol client: `GET VAR <ups> <var>`, timeouts, reconnect | net |
| `internal/policy` | Pure state machine: `Step(state, sample, now) (state, decision)` | nothing |
| `internal/actuator` | `Actuator` interface; `LogOnly` (default) and `Poweroff` | os/exec |
| `cmd/ups-guard` | Flags, poll loop, wiring | the above |

### Policy

Sample = {OnBattery bool, Charge float, Valid bool}. `OnBattery` is true when
`ups.status` contains `OB`.

- **Armed** (initial): condition = OnBattery AND Charge < 55. If the condition holds
  continuously for 120 s, decide `Shutdown` and move to **Triggered**. Any sample that
  breaks the condition resets the timer.
- **Triggered**: emits no further decisions. Returns to Armed only when
  not OnBattery AND Charge > 60 (re-arm hysteresis, 5 points above the trigger).
- **Invalid sample** (NUT unreachable or variable missing): the timer is not advanced
  and not reset; a warning is logged. If invalid for longer than `--stale-after`
  (default 300 s) while the last valid sample was OnBattery, log an error. Invalid
  data alone never triggers a shutdown.

Defaults, all flags: `--trigger-below 55`, `--rearm-above 60`, `--debounce 120s`,
`--poll 5s`, `--ups ecoflow@127.0.0.1:3493`, `--stale-after 300s`.

### Actuator

- `LogOnly` logs `WOULD shut down: on battery, charge X% < 55% for 120s`.
- `Poweroff` runs `systemctl poweroff` and is only constructed when
  `--enable-shutdown` is set. The daemon is not enabled as a service during development.

## Testing

- `policy`: table tests for outage, glitch shorter than debounce, recovery, hover at the
  threshold, re-arm, invalid samples.
- `nut`: tested against an in-process fake `upsd` socket (normal, timeout, error replies).
- `actuator`: `Poweroff` takes an injectable command runner; tests assert the call
  without executing it.
- Real-outage behaviour is validated later by the user at a time of their choosing.

## System setup (needs the user's sudo, each step reviewed first)

1. udev rule granting group `nut` access to `3746:ffff` (replaces today's temporary
   `chmod` on `/dev/bus/usb/007/010`).
2. `/etc/nut/nut.conf` (`MODE=standalone`), `ups.conf` (APC subdriver, as above),
   `upsd.conf`/`upsd.users` (loopback only, read-only user).
3. Optional systemd unit for `ups-guard`, installed disabled.

## Risks

- NUT's tolerance of the truncated descriptor was tested only on mains.
- `OB` mapping under the APC subdriver is assumed, not observed.
- Both are covered by the planned real-outage check before enabling shutdown.

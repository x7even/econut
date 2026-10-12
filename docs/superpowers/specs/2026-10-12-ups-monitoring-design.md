# econut-guard monitoring: status, TUI, Prometheus, serial telemetry

Date: 2026-10-12. Status: draft for user review. Builds on
`2026-10-12-econut-guard-design.md` (v1 shutdown guard, not yet implemented).

## Purpose

Make the EcoFlow's state visible: a live status snapshot, a terminal monitor in
the style of roctop, and a Prometheus exporter. Where the standard NUT data is
too thin (no load, watts or temperatures), decode the EcoFlow serial telemetry
stream to fill the gap.

## Hard rule

The shutdown path (v1) must not depend on anything in this document. A crash,
bug or slow client in the snapshot server, TUI, exporter or serial decoder must
never delay, block or prevent a shutdown decision. Consumers read; they never
write to the guard.

## Data sources

| Source | Gives | Status |
|---|---|---|
| NUT (`usbhid-ups`, APC HID subdriver) | charge, runtime, status flags, limits, identity | Verified. No load, watts, input voltage or temperature. `battery.voltage` (13.0 V) is not the real pack voltage. |
| EcoFlow serial (`/dev/ttyACM0`, CDC-ACM) | unknown; possibly watts in/out, temperatures, cell data | In progress (see below). |

## Serial telemetry (investigation)

Observed so far (all while on mains, 100% charge, sampled passively; nothing was
ever sent to the unit):

- The unit pushes a ~36-37 byte frame about every 30 s with no polling.
- Frame layout: `AA 03 <len u16 LE> <crc8>` then 13 more header bytes, then a body of
  17 bytes, then a 2-byte trailer (probably CRC16).
- Header byte 5 is stable (`0x2c`). Header byte 6 changes every frame (`CF, D0, D1,
  D2, D6, D7, ...`) and **is the XOR key for the body**: XOR-ing the body with it turns
  the mass of repeated key bytes into zeros and leaves structured fields. It
  usually rises by one per frame but once jumped by four, so some frames are not
  reaching us or the counter ticks for other traffic.
- Header byte 7 read `0x64` in the early frames and `0x65` later with charge fixed
  at 100%. An earlier note that it equals the charge was wrong; it looks like a slow
  counter.
- Decoded bodies look like `0f 02 01 04 63 0d 03 00 00 00 00 00 00 01 00 64 0d`. A
  16-bit little-endian value appears at both the start and end of the variable part
  (`63 0d` ... `64 0d` = 3427 ... 3428, then `42 15` ... `43 15` = 5442 ... 5443, then
  `4a 15` ... `4a 15`). Its meaning is unknown. It changed between frames while
  the system was idle, so it is not load.

These are hypotheses from a handful of frames on a quiet system. A field only
counts as decoded when it tracks a known change in the real world.

### Capture plan

1. Passive 2-hour capture to a timestamped hex log (running; `tmp/cap/serial-1.log`).
   Each line: `<unix ts> <hex bytes>`.
2. Controlled-change captures, **only with the user's explicit go-ahead for each**
   (these touch the power chain): note the wall-clock time and the change, e.g.
   plug in a measured load (a kettle or heater at a known wattage), unplug it,
   toggle an output, change the app's settings. Decoded fields that move with the
   load, and by the right amount, are watts.
3. Never test with a real outage while other work is running on the rig.

### Decoder deliverable

A package `internal/ecoflow` with `Decode(frame []byte) (Telemetry, error)`:
validates start byte, version, length and both checksums; un-XORs the body; maps
only the fields confirmed in step 2 into named values. Unknown bytes are kept in
`Telemetry.Raw` and are never presented as metrics. Table-tested against captured
frames saved as test fixtures. A frame that fails validation is dropped and counted,
never guessed at.

If the capture does not yield confident fields (checksums can't be reproduced,
or no field correlates with load), this phase ends with a written finding and the
rest of the document proceeds on NUT data only.

## Status snapshot (shared data layer)

- `internal/status`: a `Snapshot` struct (timestamp, NUT values, serial telemetry
  if available, guard state: armed/triggered, held time, last error) built from the
  guard's most recent poll.
- NUT client change: fetch all variables with `LIST VAR` (not just `ups.status` and
  `battery.charge`) and keep them in `nut.Reading.Vars map[string]string`.
- Published read-only at `http://127.0.0.1:<port>/status` (JSON) and, for the TUI,
  the same endpoint. Bound to loopback only. Served from a goroutine with its own
  timeouts; the poll loop only does a non-blocking store of the latest snapshot.
- If no consumer ever connects, nothing changes.

## TUI (`ups-top`)

- Separate command and Go module so the guard keeps its standard-library-only
  constraint. Reads `/status` once a second.
- Shows: charge bar and history sparkline, runtime remaining, state (mains /
  battery), guard state and time-to-trigger when on battery, and decoded watts and
  temperatures if the serial phase succeeds.
- Layout and keys modelled on roctop. The user will point to specifics when this
  phase starts.
- Read-only. It cannot arm, disarm or trigger anything.

## Prometheus exporter

- `/metrics` on loopback by default, text exposition format written by hand
  (no client library).
- Metrics: `ups_battery_charge_percent`, `ups_battery_runtime_seconds`,
  `ups_on_battery` (0/1), `ups_guard_triggered` (0/1), `ups_guard_held_seconds`,
  `ups_poll_errors_total`, `ups_info{model,serial}` (gauge 1), and, only if decoded,
  watts and temperatures.
- Same snapshot as the TUI; no separate NUT connection.
- Prometheus scraping and Grafana are the user's to configure; out of scope here.

## Build order

1. v1 guard (existing plan). Includes the `Vars` map in `nut.Reading`.
2. Serial capture and decode, in parallel with 1 (read-only, no overlap in code).
3. Snapshot layer, then `/metrics` (small), then the TUI.
4. Add decoded serial fields to snapshot, TUI and metrics as they are confirmed.

## Risks

- The serial protocol is proprietary and undocumented; decoding may stall. Mitigation:
  the rest of the project does not depend on it.
- EcoFlow firmware updates can change the frame format. Mitigation: strict
  validation, drop-and-count on mismatch, no guessing.
- The snapshot HTTP server is new network surface. Mitigation: loopback only, GET only,
  no input parsing beyond the path, own goroutine and timeouts.

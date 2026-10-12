# econut

Safe shutdown for a machine powered through an EcoFlow Delta 3 Ultra Plus. Plug the
EcoFlow's USB-A port into the machine; [NUT](https://networkupstools.org/) reads the
battery, and `econut-guard` powers the machine off cleanly when it is **on battery and
below your chosen charge level for a set time** (default 55% for 120 s).

- `econut-guard` is the daemon. It is **dry-run by default**: it only logs what it would
  do. A real shutdown needs `--enable-shutdown`.
- `econut status` tells you whether the guard is running, the battery level, and how
  close it is to acting.

```
$ econut status
econut-guard: running, dry-run (will NOT shut down)
UPS ecoflow@127.0.0.1:3493: on battery (OB DISCHRG), battery 48%
Shuts down when: on battery and below 55% for 120s
Condition: MET for 35s of 120s, would shut down in 85s
```

Pick your own level at install time with `--trigger-below N`. See
[deploy/README.md](deploy/README.md) to install, and `docs/` for the design.

Go standard library only. `go test ./...` runs the suite; no test can power anything off.

MIT licensed.

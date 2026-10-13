// Package econut carries the deployment files (udev rule, NUT config, systemd
// unit, install script) inside the binaries, so `econut install` needs no
// repository checkout.
package econut

import "embed"

//go:embed deploy
var Deploy embed.FS

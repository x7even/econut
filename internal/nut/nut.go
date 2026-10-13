// Package nut is a minimal client for the NUT upsd text protocol.
package nut

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPort    = "3493"
	defaultTimeout = 5 * time.Second
)

// Reading is one snapshot of the UPS. Status and Charge drive the guard; Vars
// is every variable upsd reports, for monitoring only (nil if unavailable).
type Reading struct {
	Status string            // ups.status, e.g. "OL" or "OB DISCHRG"
	Charge float64           // battery.charge, percent
	Vars   map[string]string // LIST VAR, best effort
}

const maxVars = 2000 // sanity bound on a LIST VAR reply

var unescape = strings.NewReplacer(`\"`, `"`, `\\`, `\`)

type Client struct {
	Addr    string // host:port of upsd
	UPS     string // UPS name in ups.conf
	Timeout time.Duration
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}

// Read opens one connection and fetches ups.status and battery.charge. Any
// protocol problem, error reply or non-numeric charge is returned as an error.
func (c *Client) Read(ctx context.Context) (Reading, error) {
	d := net.Dialer{Timeout: c.timeout()}
	conn, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return Reading{}, fmt.Errorf("nut: connect %s: %w", c.Addr, err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(c.timeout()))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	br := bufio.NewReader(conn)
	status, err := get(conn, br, c.UPS, "ups.status")
	if err != nil {
		return Reading{}, err
	}
	chargeStr, err := get(conn, br, c.UPS, "battery.charge")
	if err != nil {
		return Reading{}, err
	}
	charge, err := strconv.ParseFloat(chargeStr, 64)
	if err != nil {
		return Reading{}, fmt.Errorf("nut: battery.charge %q: %w", chargeStr, err)
	}
	// A garbage value (NaN, negative, over 100) must not reach the policy:
	// a negative charge would look "low" and could trigger a shutdown.
	if math.IsNaN(charge) || charge < 0 || charge > 100 {
		return Reading{}, fmt.Errorf("nut: battery.charge %q out of range 0-100", chargeStr)
	}
	// Best effort: monitoring data must never fail a read the guard depends on.
	vars, _ := listVars(conn, br, c.UPS)
	return Reading{Status: status, Charge: charge, Vars: vars}, nil
}

func listVars(w io.Writer, r *bufio.Reader, ups string) (map[string]string, error) {
	if _, err := fmt.Fprintf(w, "LIST VAR %s\n", ups); err != nil {
		return nil, err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if strings.TrimRight(line, "\r\n") != "BEGIN LIST VAR "+ups {
		return nil, fmt.Errorf("nut: unexpected reply %q", line)
	}
	vars := make(map[string]string)
	prefix := "VAR " + ups + " "
	for len(vars) <= maxVars {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "END LIST VAR "+ups {
			return vars, nil
		}
		rest, ok := strings.CutPrefix(line, prefix)
		name, val, ok2 := strings.Cut(rest, " ")
		if !ok || !ok2 || len(val) < 2 || val[0] != '"' || val[len(val)-1] != '"' {
			return nil, fmt.Errorf("nut: malformed variable line %q", line)
		}
		vars[name] = unescape.Replace(val[1 : len(val)-1])
	}
	return nil, fmt.Errorf("nut: more than %d variables", maxVars)
}

func get(w io.Writer, r *bufio.Reader, ups, name string) (string, error) {
	if _, err := fmt.Fprintf(w, "GET VAR %s %s\n", ups, name); err != nil {
		return "", fmt.Errorf("nut: write: %w", err)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("nut: read %s: %w", name, err)
	}
	line = strings.TrimRight(line, "\r\n")
	if rest, ok := strings.CutPrefix(line, "ERR "); ok {
		return "", fmt.Errorf("nut: %s %s: %s", ups, name, rest)
	}
	rest, ok := strings.CutPrefix(line, fmt.Sprintf("VAR %s %s \"", ups, name))
	if !ok || !strings.HasSuffix(rest, "\"") {
		return "", fmt.Errorf("nut: unexpected reply %q", line)
	}
	return strings.TrimSuffix(rest, "\""), nil
}

// ParseTarget splits "name@host[:port]" into the UPS name and a host:port
// address, defaulting the port to 3493.
func ParseTarget(s string) (ups, addr string, err error) {
	ups, host, ok := strings.Cut(s, "@")
	if !ok || ups == "" || host == "" || strings.ContainsAny(s, " \t") {
		return "", "", fmt.Errorf("invalid UPS target %q, want name@host[:port]", s)
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, defaultPort)
	}
	return ups, host, nil
}

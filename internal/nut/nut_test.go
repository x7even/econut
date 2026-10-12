package nut

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"time"
)

// fakeUPSD serves NUT-style replies. handle gets each command line and
// returns the raw reply; ok=false closes the connection.
func fakeUPSD(t *testing.T, handle func(cmd string) (reply string, ok bool)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					reply, ok := handle(sc.Text())
					if !ok {
						return
					}
					fmt.Fprint(c, reply)
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func normal(status, charge string) func(string) (string, bool) {
	return func(cmd string) (string, bool) {
		switch cmd {
		case "GET VAR ecoflow ups.status":
			return fmt.Sprintf("VAR ecoflow ups.status \"%s\"\n", status), true
		case "GET VAR ecoflow battery.charge":
			return fmt.Sprintf("VAR ecoflow battery.charge \"%s\"\n", charge), true
		}
		return "ERR UNKNOWN-COMMAND\n", true
	}
}

func TestReadNormal(t *testing.T) {
	addr := fakeUPSD(t, normal("OB DISCHRG", "73"))
	c := &Client{Addr: addr, UPS: "ecoflow", Timeout: time.Second}
	got, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "OB DISCHRG" || got.Charge != 73 {
		t.Fatalf("got %+v", got)
	}
}

func withList(base func(string) (string, bool), list string) func(string) (string, bool) {
	return func(cmd string) (string, bool) {
		if cmd == "LIST VAR ecoflow" {
			return list, true
		}
		return base(cmd)
	}
}

func TestReadFillsVars(t *testing.T) {
	list := "BEGIN LIST VAR ecoflow\n" +
		"VAR ecoflow battery.runtime \"11800\"\n" +
		"VAR ecoflow device.model \"A \\\"quoted\\\" \\\\ model\"\n" +
		"END LIST VAR ecoflow\n"
	addr := fakeUPSD(t, withList(normal("OL", "100"), list))
	c := &Client{Addr: addr, UPS: "ecoflow", Timeout: time.Second}
	got, err := c.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Vars["battery.runtime"] != "11800" {
		t.Fatalf("Vars = %v", got.Vars)
	}
	if want := `A "quoted" \ model`; got.Vars["device.model"] != want {
		t.Fatalf("device.model = %q, want %q", got.Vars["device.model"], want)
	}
}

// A broken LIST VAR must not fail the read: the shutdown path only needs
// status and charge.
func TestReadSurvivesBrokenList(t *testing.T) {
	for name, list := range map[string]string{
		"err reply": "ERR ACCESS-DENIED\n",
		"truncated": "BEGIN LIST VAR ecoflow\nVAR ecoflow a \"1\"\n",
		"malformed": "BEGIN LIST VAR ecoflow\nVAR ecoflow a 1\nEND LIST VAR ecoflow\n",
		"garbage":   "HELLO\n",
	} {
		t.Run(name, func(t *testing.T) {
			addr := fakeUPSD(t, withList(normal("OB DISCHRG", "40"), list))
			c := &Client{Addr: addr, UPS: "ecoflow", Timeout: time.Second}
			got, err := c.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != "OB DISCHRG" || got.Charge != 40 {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestReadErrorsBecomeErrors(t *testing.T) {
	cases := map[string]func(string) (string, bool){
		"data stale":     func(string) (string, bool) { return "ERR DATA-STALE\n", true },
		"non-numeric":    normal("OL", "abc"),
		"empty charge":   normal("OL", ""),
		"closed at once": func(string) (string, bool) { return "", false },
		"garbage reply":  func(string) (string, bool) { return "HELLO\n", true },
		"wrong ups name": func(string) (string, bool) { return "VAR other ups.status \"OL\"\n", true },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			addr := fakeUPSD(t, h)
			c := &Client{Addr: addr, UPS: "ecoflow", Timeout: time.Second}
			if _, err := c.Read(context.Background()); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestReadTimesOutOnHang(t *testing.T) {
	addr := fakeUPSD(t, func(string) (string, bool) {
		time.Sleep(2 * time.Second)
		return "", false
	})
	c := &Client{Addr: addr, UPS: "ecoflow", Timeout: 150 * time.Millisecond}
	start := time.Now()
	if _, err := c.Read(context.Background()); err == nil {
		t.Fatal("want timeout error")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s, timeout not honoured", d)
	}
}

func TestReadHonoursContextCancel(t *testing.T) {
	addr := fakeUPSD(t, func(string) (string, bool) {
		time.Sleep(2 * time.Second)
		return "", false
	})
	c := &Client{Addr: addr, UPS: "ecoflow", Timeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Read(ctx); err == nil {
		t.Fatal("want error")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s, cancel not honoured", d)
	}
}

func TestReadConnectionRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c := &Client{Addr: addr, UPS: "ecoflow", Timeout: time.Second}
	if _, err := c.Read(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestParseTarget(t *testing.T) {
	ok := map[string][2]string{
		"ecoflow@127.0.0.1:3493": {"ecoflow", "127.0.0.1:3493"},
		"ecoflow@localhost":      {"ecoflow", "localhost:3493"},
		"ups1@10.0.0.5:13493":    {"ups1", "10.0.0.5:13493"},
	}
	for in, want := range ok {
		ups, addr, err := ParseTarget(in)
		if err != nil || ups != want[0] || addr != want[1] {
			t.Errorf("ParseTarget(%q) = %q, %q, %v; want %q, %q", in, ups, addr, err, want[0], want[1])
		}
	}
	for _, in := range []string{"", "ecoflow", "@host", "name@", "a b@host"} {
		if _, _, err := ParseTarget(in); err == nil {
			t.Errorf("ParseTarget(%q): want error", in)
		}
	}
}

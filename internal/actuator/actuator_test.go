package actuator

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestLogOnlyLogsAndDoesNothingElse(t *testing.T) {
	log, buf := testLogger()
	if err := (LogOnly{Log: log}).Shutdown(context.Background(), "because"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "WOULD shut down") || !strings.Contains(out, "because") {
		t.Fatalf("log = %q", out)
	}
}

func TestPoweroffRunsSystemctlPoweroff(t *testing.T) {
	log, _ := testLogger()
	var gotName string
	var gotArgs []string
	p := Poweroff{Log: log, Run: func(_ context.Context, name string, args ...string) error {
		gotName, gotArgs = name, args
		return nil
	}}
	if err := p.Shutdown(context.Background(), "r"); err != nil {
		t.Fatal(err)
	}
	if gotName != "systemctl" || len(gotArgs) != 1 || gotArgs[0] != "poweroff" {
		t.Fatalf("ran %q %v", gotName, gotArgs)
	}
}

func TestPoweroffPropagatesRunnerError(t *testing.T) {
	log, _ := testLogger()
	want := errors.New("boom")
	p := Poweroff{Log: log, Run: func(context.Context, string, ...string) error { return want }}
	if err := p.Shutdown(context.Background(), "r"); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

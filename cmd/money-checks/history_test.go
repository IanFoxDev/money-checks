package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const historyFile = `{"time":"2026-09-01T00:00:00Z","check":"refund_twice","id":"r0","event":"baseline","scope":"full","version":"aaaa0000"}
{"time":"2026-10-01T00:00:00Z","check":"refund_twice","id":"r1","event":"new","scope":"window","version":"aaaa0000"}
{"time":"2026-10-01T00:00:00Z","check":"refund_twice","id":"r1","event":"new","scope":"window","version":"aaaa0000"}
{"time":"2026-10-03T00:00:00Z","check":"refund_twice","id":"r1","event":"fixed","scope":"full","version":"aaaa0000"}
{"time":"2026-10-05T00:00:00Z","check":"refund_twice","id":"r2","event":"new","scope":"window","version":"aaaa0000"}
{"time":"2026-10-06T00:00:00Z","check":"refund_twice","id":"r2","event":"fixed","scope":"full","version":"aaaa0000"}
{"time":"2026-10-07T00:00:00Z","check":"refund_twice","id":"r1","event":"regressed","scope":"window","version":"aaaa0000"}
{"time":"2026-10-07T12:00:00Z","check":"refund_twice","id":"r1","event":"changed","scope":"window","version":"aaaa0000"}
{"time":"2026-10-07T00:00:00Z","check":"stuck","id":"s1","event":"baseline","scope":"full","version":"bbbb0000"}
{"time":"2026-10-07T00:00:00Z","check":"stuck","id":"s2","event":"baseline","scope":"full","version":"bbbb0000"}
{"time":"2026-10-08T00:00:00Z","check":"stuck","id":"s1","event":"dropped","scope":"full","version":"cccc0000"}
{"time":"2026-10-08T00:00:00Z","check":"stuck","id":"s3","event":"baseline","scope":"full","version":"cccc0000"}
`

func historyCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte(historyFile), 0o600); err != nil {
		t.Fatal(err)
	}
	return runCLI(t, append([]string{"history", "-f", path}, args...)...)
}

// fixed() is 2026-10-08 06:00 UTC: the default 720h starts on 2026-09-08, after
// the baseline of r0, which still counts as open.
func TestHistory(t *testing.T) {
	code, out, errOut := historyCLI(t)
	if code != exitOK {
		t.Fatalf("code %d: %s", code, errOut)
	}
	for _, want := range []string{
		"2026-09-08 06:00 to 2026-10-08 06:00 UTC, 11 events",
		"check  new  changed  came back  fixed  open  oldest open  median to fix",
		"refund_twice    2        1          1      2     2          37d            36h",
		"stuck    0        0          0      0     2          30h              -",
		"stuck bbbb0000, 2026-10-07 00:00: 2 found for the first time, 0 no longer found",
		"stuck cccc0000, 2026-10-08 00:00: 1 found for the first time, 1 no longer found",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestHistoryJSON(t *testing.T) {
	code, out, errOut := historyCLI(t, "--json", "--check", "refund_twice", "--since", "48h")
	if code != exitOK {
		t.Fatalf("code %d: %s", code, errOut)
	}
	var got struct {
		Checks []struct {
			Check       string     `json:"check"`
			Changed     int        `json:"changed"`
			Regressed   int        `json:"regressed"`
			Fixed       int        `json:"fixed"`
			Open        int        `json:"open"`
			Oldest      *time.Time `json:"oldest_open"`
			MedianToFix float64    `json:"median_to_fix_seconds"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Checks) != 1 {
		t.Fatalf("%s", out)
	}
	c := got.Checks[0]
	if c.Check != "refund_twice" || c.Regressed != 1 || c.Changed != 1 || c.Fixed != 0 || c.Open != 2 || c.MedianToFix != 0 || !c.Oldest.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("%+v", c)
	}
}

func TestHistoryErrors(t *testing.T) {
	for _, args := range [][]string{
		{"history"},
		{"history", "-f", filepath.Join(t.TempDir(), "none.jsonl")},
	} {
		if code, _, _ := runCLI(t, args...); code != exitError {
			t.Errorf("%v: code %d", args, code)
		}
	}
	if code, _, errOut := historyCLI(t, "--check", "other"); code != exitError || !strings.Contains(errOut, "no events of check other") {
		t.Errorf("code %d: %s", code, errOut)
	}
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	_ = os.WriteFile(path, []byte("{}\n"+historyFile), 0o600)
	if code, _, errOut := runCLI(t, "history", "-f", path); code != exitError || !strings.Contains(errOut, "line 1 is not an event") {
		t.Errorf("code %d: %s", code, errOut)
	}
}

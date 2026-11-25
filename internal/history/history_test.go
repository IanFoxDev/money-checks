package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	at := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	a := &Amount{Currency: "EUR", Minor: 999, Exponent: 2}
	if err := Append(path, []Event{{Time: at, Check: "c", ID: "p1", Event: New, Scope: "window", Amount: a, Version: "3f9a1c0e"}}); err != nil {
		t.Fatal(err)
	}
	// A run written again after a crash, then a change.
	if err := Append(path, []Event{{Time: at, Check: "c", ID: "p1", Event: New, Amount: a}, {Time: at.Add(time.Hour), Check: "c", ID: "p1", Event: Fixed, Amount: a}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if line := strings.SplitN(string(data), "\n", 2)[0]; line != `{"time":"2026-11-25T10:00:00Z","check":"c","id":"p1","event":"new","scope":"window","amount":{"currency":"EUR","minor":999,"exponent":2},"version":"3f9a1c0e"}` {
		t.Errorf("line %s", line)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	es, err := Read(strings.NewReader(string(data) + `{"time":"2026-11-25T12:00:00Z","check":"c","id":"p`))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0].Event != New || es[1].Event != Fixed || !es[0].Open() || es[1].Open() {
		t.Errorf("%+v", es)
	}
	if _, err := Read(strings.NewReader("not json\n" + string(data))); err == nil || err.Error() != "line 1 is not an event" {
		t.Errorf("err %v", err)
	}
}

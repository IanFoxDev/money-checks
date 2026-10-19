package serve

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// serverWithState is a server whose state file is path, as Run would start it.
func serverWithState(t *testing.T, path string, src *script) (*Server, *notes) {
	t.Helper()
	s, n := newServer(t, src)
	s.cfg.Serve.StateFile = path
	s.load()
	return s, n
}

// A restart with the state file posts only what changed while serve was down.
func TestStateFileSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, n1 := serverWithState(t, path, &script{runs: [][]source.Row{{r("u1", 999, "USD")}}})
	first.Cycle(context.Background())
	if len(n1.msgs) != 1 {
		t.Fatalf("first start: %q", n1.msgs)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"u1":{"amount":{"currency":"USD","minor":999,"exponent":2},"first_seen":"2026-10-07T`) {
		t.Errorf("state file:\n%s", data)
	}

	again, n2 := serverWithState(t, path, &script{runs: [][]source.Row{
		{r("u1", 999, "USD")},
		{r("u1", 999, "USD"), r("u2", 500, "USD")},
	}})
	again.Cycle(context.Background())
	again.Cycle(context.Background())
	want := "money-checks: two_active_subscriptions found 1 new violation (5.00 USD), 2 in all [error]"
	if strings.Join(n2.msgs, "\n") != want {
		t.Errorf("after the restart: %q", n2.msgs)
	}
	if v := again.states["two_active_subscriptions"].seen["u1"]; !v.first.Equal(time.Unix(1791400000, 0)) || v.amount.String() != "9.99 USD" {
		t.Errorf("u1 %+v", v)
	}
}

// A file that cannot be read is a warning: serve starts as without it.
func TestStateFileUnreadable(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"broken.json":  `{"version": 1, "checks": {`,
		"version.json": `{"version": 2, "checks": {"two_active_subscriptions": {"complete": true, "found": {"full": true}}}}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		var log strings.Builder
		s, n := newServer(t, &script{runs: [][]source.Row{{r("u1", 999, "USD")}}})
		s.cfg.Serve.StateFile, s.log = path, slog.New(slog.NewTextHandler(&log, nil))
		s.load()
		s.Cycle(context.Background())
		if len(n.msgs) != 1 || !strings.Contains(log.String(), "the state file cannot be read") {
			t.Errorf("%s: messages %q, log %s", name, n.msgs, log.String())
		}
	}
}

// A check that is no longer in the configuration is dropped from the file.
func TestStateFileDropsOldChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	body := `{"version": 1, "checks": {"gone": {"complete": true, "violations": {"x": {"first_seen": "2026-10-01T00:00:00Z"}}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := serverWithState(t, path, &script{runs: [][]source.Row{{}}})
	s.Cycle(context.Background())
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "gone") {
		t.Errorf("state file:\n%s", data)
	}
}

// serve does not start when it cannot write the state file.
func TestRunNeedsAWritableStateFile(t *testing.T) {
	s, _ := newServer(t, &script{runs: [][]source.Row{{}}})
	s.cfg.Serve.StateFile = filepath.Join(t.TempDir(), "missing", "state.json")
	s.cfg.Serve.Listen = "127.0.0.1:0"
	err := s.Run(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "serve.state_file: ") {
		t.Errorf("got %v", err)
	}
}

func TestStateFileIsRelativeToTheConfiguration(t *testing.T) {
	for path, want := range map[string]string{"state.json": "/etc/mc/state.json", "/var/lib/mc/state.json": "/var/lib/mc/state.json"} {
		c, err := config.Parse([]byte(conf+"serve: {state_file: "+path+"}\n"), "/etc/mc")
		if err != nil {
			t.Fatal(err)
		}
		if c.Serve.StateFile != want {
			t.Errorf("%s: %s", path, c.Serve.StateFile)
		}
	}
}

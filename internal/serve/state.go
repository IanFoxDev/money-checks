package serve

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/history"
)

// stateVersion is the version of the state file. Version 1, without versions of
// checks and fixed violations, is read too; another version is not.
const stateVersion = 2

// stateFile is what serve.state_file holds: per check, what the last runs found.
// See docs/adr/0006-serve-remembers-violations.md.
type stateFile struct {
	Version int                   `json:"version"`
	Checks  map[string]checkState `json:"checks"`
}

type checkState struct {
	Failed bool `json:"failed,omitempty"`
	// Found is per scope, "full" and "window".
	Found map[string]bool `json:"found,omitempty"`
	// Complete says that Violations holds every violation of the last full run.
	Complete   bool                      `json:"complete,omitempty"`
	Violations map[string]savedViolation `json:"violations,omitempty"`
	// Version is the version of the check that made the last full run.
	Version string                `json:"version,omitempty"`
	Fixed   map[string]savedFixed `json:"fixed,omitempty"`
}

type savedFixed struct {
	Amount    *savedAmount `json:"amount,omitempty"`
	FirstSeen time.Time    `json:"first_seen"`
	FixedAt   time.Time    `json:"fixed_at"`
}

type savedViolation struct {
	Amount    *savedAmount `json:"amount,omitempty"`
	FirstSeen time.Time    `json:"first_seen"`
}

type savedAmount struct {
	Currency string `json:"currency"`
	Minor    int64  `json:"minor"`
	Exponent int    `json:"exponent"`
}

// load reads the state file into the server. A file that does not exist is a
// first start. A file that cannot be read is a warning, and serve starts as
// without it: one noisy run is better than monitoring that does not start.
// Checks that are no longer in the configuration are dropped.
func (s *Server) load() {
	path := s.cfg.Serve.StateFile
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var f stateFile
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	if err == nil && f.Version != stateVersion && f.Version != 1 {
		err = fmt.Errorf("version %d, want %d", f.Version, stateVersion)
	}
	if err != nil {
		s.log.Warn("the state file cannot be read; starting without it, so this run posts every check that is not clean", "file", path, "error", err)
		return
	}
	names := map[string]bool{}
	for _, ch := range s.cfg.Checks {
		names[ch.Name] = true
	}
	for name, cs := range f.Checks {
		if !names[name] {
			continue
		}
		st := &state{failed: cs.Failed, found: map[check.Scope]bool{}, complete: cs.Complete}
		for scope, found := range cs.Found {
			st.found[check.Scope(scope)] = found
		}
		if cs.Complete {
			st.seen = make(map[string]violation, len(cs.Violations))
			for id, v := range cs.Violations {
				st.seen[id] = violation{first: v.FirstSeen, amount: v.Amount.money()}
			}
			st.version = cs.Version
			if len(cs.Fixed) > 0 {
				st.fixed = make(map[string]fixedViolation, len(cs.Fixed))
				for id, v := range cs.Fixed {
					st.fixed[id] = fixedViolation{amount: v.Amount.money(), first: v.FirstSeen, at: v.FixedAt}
				}
			}
		}
		s.states[name] = st
	}
}

// save writes the state file: to a temporary file next to it, then renamed, so
// a crash leaves the old file or the new one, never half of one. The file holds
// ids and amounts of violations, as the reports do, so it is readable by its
// owner only: os.CreateTemp makes it 0600.
func (s *Server) save() error {
	path := s.cfg.Serve.StateFile
	f := stateFile{Version: stateVersion, Checks: map[string]checkState{}}
	for name, st := range s.states {
		cs := checkState{Failed: st.failed, Complete: st.complete, Found: map[string]bool{}}
		for scope, found := range st.found {
			cs.Found[string(scope)] = found
		}
		if st.complete {
			cs.Violations = make(map[string]savedViolation, len(st.seen))
			for id, v := range st.seen {
				cs.Violations[id] = savedViolation{FirstSeen: v.first, Amount: saved(v.amount)}
			}
			cs.Version = st.version
			if len(st.fixed) > 0 {
				cs.Fixed = make(map[string]savedFixed, len(st.fixed))
				for id, v := range st.fixed {
					cs.Fixed[id] = savedFixed{Amount: saved(v.amount), FirstSeen: v.first, FixedAt: v.at}
				}
			}
		}
		f.Checks[name] = cs
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	return err
}

func (a *savedAmount) money() *check.Money {
	if a == nil {
		return nil
	}
	return &check.Money{Currency: a.Currency, Minor: a.Minor, Exponent: a.Exponent}
}

func saved(m *check.Money) *savedAmount {
	if m == nil {
		return nil
	}
	return &savedAmount{Currency: m.Currency, Minor: m.Minor, Exponent: m.Exponent}
}

// startHistory makes sure the history file can be written. A file that does not
// exist yet starts with a baseline of every violation serve already tracks, at
// the time it first saw each, so the history knows what is open from its first
// line. See docs/adr/0010-violation-history.md.
func (s *Server) startHistory() error {
	path := s.cfg.Serve.HistoryFile
	_, err := os.Stat(path)
	if err == nil {
		return history.Append(path, nil)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var events []history.Event
	for _, ch := range s.cfg.Checks {
		st := s.states[ch.Name]
		if st == nil || !st.complete {
			continue
		}
		version := s.cfg.CheckVersion(ch)
		start := len(events)
		for id, v := range st.seen {
			events = append(events, history.Event{Time: v.first, Check: ch.Name, ID: id, Event: history.Baseline, Scope: string(check.Full), Amount: amount(v.amount), Version: version})
		}
		sortEvents(events[start:])
	}
	return history.Append(path, events)
}

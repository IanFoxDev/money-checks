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
)

// stateVersion is the version of the state file; another version is not read.
const stateVersion = 1

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
	if err == nil && f.Version != stateVersion {
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
				w := violation{first: v.FirstSeen}
				if a := v.Amount; a != nil {
					w.amount = &check.Money{Currency: a.Currency, Minor: a.Minor, Exponent: a.Exponent}
				}
				st.seen[id] = w
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
				sv := savedViolation{FirstSeen: v.first}
				if a := v.amount; a != nil {
					sv.Amount = &savedAmount{Currency: a.Currency, Minor: a.Minor, Exponent: a.Exponent}
				}
				cs.Violations[id] = sv
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

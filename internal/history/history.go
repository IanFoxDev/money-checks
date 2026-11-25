// Package history is the log of what happened to each violation that serve
// tracks: one line of JSON per change, appended to serve.history_file. See
// docs/adr/0010-violation-history.md.
package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// The events of a violation.
const (
	New       = "new"       // an id not seen before
	Changed   = "changed"   // an id seen before, with another amount
	Fixed     = "fixed"     // an id a full run no longer finds
	Regressed = "regressed" // an id fixed before and found again
	Baseline  = "baseline"  // an id found by the first full run of a new or changed check
	Dropped   = "dropped"   // an id a changed check no longer finds
)

// Event is one line of the history file.
type Event struct {
	Time    time.Time `json:"time"`
	Check   string    `json:"check"`
	ID      string    `json:"id"`
	Event   string    `json:"event"`
	Scope   string    `json:"scope,omitempty"`
	Amount  *Amount   `json:"amount,omitempty"`
	Version string    `json:"version,omitempty"`
}

// Amount is an amount in minor units of its currency, as the state file keeps it.
type Amount struct {
	Currency string `json:"currency"`
	Minor    int64  `json:"minor"`
	Exponent int    `json:"exponent"`
}

// Open says whether the event leaves the violation open.
func (e Event) Open() bool {
	switch e.Event {
	case New, Changed, Regressed, Baseline:
		return true
	}
	return false
}

// Append writes the events at the end of the file at path, one line each, and
// creates the file with mode 0600 if it does not exist: it holds ids and amounts
// of violations, as the state file does.
func Append(path string, events []Event) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range events {
		if err = enc.Encode(e); err != nil {
			break
		}
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Read reads the events of a history file in order. An event that repeats the
// last one of its id, as a run written twice after a crash does, is left out. A
// line that is not an event is an error with its number; a last line cut short,
// by a crash in the middle of a write, is left out.
func Read(r io.Reader) ([]Event, error) {
	var events []Event
	last := map[[2]string]Event{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	n := 0
	var bad error
	for sc.Scan() {
		n++
		if bad != nil {
			return nil, bad
		}
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.Check == "" || e.Event == "" {
			bad = fmt.Errorf("line %d is not an event", n)
			continue
		}
		key := [2]string{e.Check, e.ID}
		if p, ok := last[key]; ok && p.Event == e.Event && sameAmount(p.Amount, e.Amount) {
			continue
		}
		last[key] = e
		events = append(events, e)
	}
	return events, sc.Err()
}

func sameAmount(a, b *Amount) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

package history

import (
	"sort"
	"time"
)

// Summary is what happened to the violations of one check in a period.
type Summary struct {
	Check string `json:"check"`
	// Events of the period, by kind.
	New       int `json:"new"`
	Changed   int `json:"changed"`
	Regressed int `json:"regressed"`
	Fixed     int `json:"fixed"`
	Baseline  int `json:"baseline"`
	Dropped   int `json:"dropped"`
	// Open are the violations open at the end of the period; Oldest is when the
	// oldest of them was found, the start of its last time open.
	Open   int        `json:"open"`
	Oldest *time.Time `json:"oldest_open,omitempty"`
	// MedianToFix is the median time from found to fixed of the fixes of the
	// period, in seconds; 0 without fixes.
	MedianToFix float64 `json:"median_to_fix_seconds"`
	// Versions are the baselines of the period, one per version of the check.
	Versions []Version `json:"versions,omitempty"`
}

// Version is the baseline of one version of a check: when its first full run
// was and what it found that the version before did not, and what it no longer
// found.
type Version struct {
	Version  string    `json:"version"`
	Time     time.Time `json:"time"`
	Baseline int       `json:"baseline"`
	Dropped  int       `json:"dropped"`
}

// Summarize replays the events in order and counts those from since to until,
// per check, in the order of the names. Only the events up to until count for
// what is open. The baseline that starts a history file has the times the
// violations were first seen, so it counts in the period those times are in.
func Summarize(events []Event, since, until time.Time) []Summary {
	type check struct {
		s       Summary
		opened  map[string]time.Time
		toFix   []time.Duration
		version map[string]*Version
	}
	checks := map[string]*check{}
	for _, e := range events {
		if e.Time.After(until) {
			continue
		}
		c := checks[e.Check]
		if c == nil {
			c = &check{s: Summary{Check: e.Check}, opened: map[string]time.Time{}, version: map[string]*Version{}}
			checks[e.Check] = c
		}
		in := !e.Time.Before(since)
		start, open := c.opened[e.ID]
		switch e.Event {
		case New, Changed, Regressed, Baseline:
			if !open {
				c.opened[e.ID] = e.Time
			}
		case Fixed:
			delete(c.opened, e.ID)
			if in && open {
				c.toFix = append(c.toFix, e.Time.Sub(start))
			}
		case Dropped:
			delete(c.opened, e.ID)
		}
		if !in {
			continue
		}
		switch e.Event {
		case New:
			c.s.New++
		case Changed:
			c.s.Changed++
		case Regressed:
			c.s.Regressed++
		case Fixed:
			c.s.Fixed++
		case Baseline, Dropped:
			v := c.version[e.Version]
			if v == nil {
				v = &Version{Version: e.Version, Time: e.Time}
				c.version[e.Version] = v
			}
			if e.Time.Before(v.Time) {
				v.Time = e.Time
			}
			if e.Event == Baseline {
				c.s.Baseline++
				v.Baseline++
			} else {
				c.s.Dropped++
				v.Dropped++
			}
		}
	}
	out := make([]Summary, 0, len(checks))
	for _, c := range checks {
		c.s.Open = len(c.opened)
		for _, t := range c.opened {
			if c.s.Oldest == nil || t.Before(*c.s.Oldest) {
				c.s.Oldest = &t
			}
		}
		if n := len(c.toFix); n > 0 {
			sort.Slice(c.toFix, func(i, j int) bool { return c.toFix[i] < c.toFix[j] })
			m := c.toFix[n/2]
			if n%2 == 0 {
				m = (c.toFix[n/2-1] + c.toFix[n/2]) / 2
			}
			c.s.MedianToFix = m.Seconds()
		}
		for _, v := range c.version {
			c.s.Versions = append(c.s.Versions, *v)
		}
		sort.Slice(c.s.Versions, func(i, j int) bool { return c.s.Versions[i].Time.Before(c.s.Versions[j].Time) })
		out = append(out, c.s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Check < out[j].Check })
	return out
}

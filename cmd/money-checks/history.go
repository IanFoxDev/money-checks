package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/ianfoxdev/money-checks/internal/history"
)

// historyReport reads the history file of serve and prints, per check, what
// happened to its violations in a period. It connects to nothing and needs no
// configuration. See docs/adr/0010-violation-history.md.
func historyReport(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("f", "", "the history file, serve.history_file")
	since := fs.Duration("since", 720*time.Hour, "the period, up to now")
	name := fs.String("check", "", "only this check")
	asJSON := fs.Bool("json", false, "print JSON instead of text")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *path == "" || fs.NArg() > 0 || *since <= 0 {
		fmt.Fprintln(stderr, "history: -f FILE is required, and --since must be positive")
		return exitError
	}
	f, err := os.Open(*path)
	if err != nil {
		fmt.Fprintln(stderr, "history:", err)
		return exitError
	}
	events, err := history.Read(f)
	_ = f.Close()
	if err != nil {
		fmt.Fprintf(stderr, "history: %s: %v\n", *path, err)
		return exitError
	}
	if *name != "" {
		var only []history.Event
		for _, e := range events {
			if e.Check == *name {
				only = append(only, e)
			}
		}
		if len(only) == 0 {
			fmt.Fprintf(stderr, "history: no events of check %s in %s\n", *name, *path)
			return exitError
		}
		events = only
	}
	until := now().UTC()
	from := until.Add(-*since)
	sums := history.Summarize(events, from, until)
	if *asJSON {
		out := struct {
			Since  time.Time         `json:"since"`
			Until  time.Time         `json:"until"`
			Checks []history.Summary `json:"checks"`
		}{from, until, sums}
		if out.Checks == nil {
			out.Checks = []history.Summary{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(stderr, "history:", err)
			return exitError
		}
		return exitOK
	}
	fmt.Fprintf(stdout, "%s to %s UTC, %s\n\n", from.Format("2006-01-02 15:04"), until.Format("2006-01-02 15:04"), plural(int64(len(events)), "event"))
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "check\tnew\tchanged\tcame back\tfixed\topen\toldest open\tmedian to fix\t")
	for _, s := range sums {
		oldest := "-"
		if s.Oldest != nil {
			oldest = span(until.Sub(*s.Oldest))
		}
		toFix := "-"
		if s.Fixed > 0 {
			toFix = span(time.Duration(s.MedianToFix * float64(time.Second)))
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t\n", s.Check, s.New, s.Changed, s.Regressed, s.Fixed, s.Open, oldest, toFix)
	}
	_ = tw.Flush()
	first := true
	for _, s := range sums {
		for _, v := range s.Versions {
			if first {
				fmt.Fprintln(stdout, "\nBaselines (first full run of a new or changed check):")
				first = false
			}
			fmt.Fprintf(stdout, "  %s %s, %s: %d found for the first time, %d no longer found\n", s.Check, v.Version, v.Time.Format("2006-01-02 15:04"), v.Baseline, v.Dropped)
		}
	}
	return exitOK
}

// span is a duration for a person: minutes, hours up to two days, then days.
func span(d time.Duration) string {
	switch {
	case d < time.Hour:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	case d < 48*time.Hour:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
}

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
)

// defaultKnownLimit is how many new violations known add accepts at once unless
// told otherwise. A thousand rows are rarely one problem someone has looked at.
const defaultKnownLimit = 100

// knownEntry is one entry as known add writes it, in the order people read it.
type knownEntry struct {
	Check  string `yaml:"check"`
	ID     string `yaml:"id"`
	Amount string `yaml:"amount,omitempty"`
	Reason string `yaml:"reason"`
	Until  string `yaml:"until,omitempty"`
}

// knownAdd runs one check and appends every new violation it finds to the known
// file with the reason given. It runs the check again rather than reading a
// report, because a report has samples, not every row. The file goes through
// review like code, and the diff is the list of what was accepted.
func knownAdd(ctx context.Context, args []string, stdout, stderr io.Writer, open check.Opener, now func() time.Time) int {
	fset := flag.NewFlagSet("known add", flag.ContinueOnError)
	fset.SetOutput(stderr)
	path := fset.String("c", "checks.yaml", "configuration file")
	name := fset.String("check", "", "the check whose new violations are accepted (required)")
	reason := fset.String("reason", "", "why they are accepted, written into every entry (required)")
	until := fset.String("until", "", "a date like 2026-12-31: the entries apply through that day, UTC")
	limit := fset.Int("limit", defaultKnownLimit, "refuse when the check finds more new violations than this")
	if err := fset.Parse(args); err != nil {
		return exitError
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, format+"\n", a...)
		return exitError
	}
	*reason = strings.TrimSpace(*reason)
	switch {
	case *name == "":
		return fail("known add: --check is required")
	case *reason == "":
		return fail("known add: --reason is required; say why these violations are accepted")
	case *limit < 1:
		return fail("known add: --limit must be at least 1")
	}
	if *until != "" {
		if _, err := time.Parse(time.DateOnly, *until); err != nil {
			return fail("known add: --until: want a date like 2026-12-31, got %q", *until)
		}
	}
	// On the first run there is no known file yet, and the configuration does not
	// load without it. Start one; it goes away again unless entries are written.
	created, err := startKnown(*path)
	if err != nil {
		return fail("known add: %v", err)
	}
	written := false
	if created != "" {
		defer func() {
			if !written {
				_ = os.Remove(created)
			}
		}()
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return fail("%v", err)
	}
	if cfg.KnownFile == "" {
		return fail("known add: %s has no known_file; add a line like known_file: known.yaml", *path)
	}
	var ch *config.Check
	for i := range cfg.Checks {
		if cfg.Checks[i].Name == *name {
			ch = &cfg.Checks[i]
		}
	}
	if ch == nil {
		return fail("known add: no check is named %q", *name)
	}

	// Every row is a sample, up to one more than the limit, so too many is visible.
	// The run gets a copy with this check only; cfg keeps all of them, because the
	// known file it checks below has entries of every check.
	n := *limit + 1
	one := *ch
	one.Samples = &n
	runCfg := cfg
	runCfg.Checks = []config.Check{one}
	r := check.Run(ctx, runCfg, check.Full, open, now)
	for _, w := range r.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	res := r.Results[0]
	switch {
	case res.Status == check.StatusFailed:
		return fail("check %s failed: %s", res.Name, res.Error)
	case res.Violations == 0:
		fmt.Fprintf(stdout, "check %s has no new violations; nothing to add\n", res.Name)
		return exitOK
	case res.Violations > int64(*limit):
		return fail("check %s has %d new violations, more than --limit %d; that many is rarely one known problem. Raise --limit if it is", res.Name, res.Violations, *limit)
	}

	var entries []knownEntry
	for _, s := range res.Samples {
		if s.Note != "" {
			// The id is listed already; a second entry would be an error.
			fmt.Fprintf(stderr, "skipped %s: %s; edit its entry in %s\n", s.ID, s.Note, cfg.KnownFile)
			continue
		}
		e := knownEntry{Check: res.Name, ID: s.ID, Reason: *reason, Until: *until}
		if s.Amount != nil {
			e.Amount = s.Amount.String()
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		fmt.Fprintf(stdout, "check %s has no new violations that are not listed already; nothing to add\n", res.Name)
		return exitOK
	}
	text, err := knownText(entries, r.StartedAt)
	if err != nil {
		return fail("known add: %v", err)
	}

	file := config.Path(filepath.Dir(*path), cfg.KnownFile)
	data, err := os.ReadFile(file)
	if err != nil {
		return fail("known add: %v", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, text...)
	// Appending works when the known list is the last thing in the file and is
	// written as a block. Read the result back before writing it.
	known, err := cfg.ParseKnown(data)
	if err == nil && count(known) != count(cfg.Known)+len(entries) {
		err = errors.New("the new entries did not land in the known list")
	}
	if err != nil {
		fmt.Fprintf(stderr, "known add: cannot append to %s (%v).\nThe known list must be the last key of the file, one entry per item. Add these entries by hand:\n\n", cfg.KnownFile, err)
		_, _ = io.WriteString(stderr, text)
		return exitError
	}
	if err := writeAtomic(file, data); err != nil {
		return fail("known add: %v", err)
	}
	written = true
	fmt.Fprintf(stdout, "added %d entries for check %s to %s\n", len(entries), res.Name, cfg.KnownFile)
	return exitOK
}

// startKnown creates the known file the configuration names when it does not
// exist, and returns its path; "" when there was nothing to create.
func startKnown(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var c struct {
		KnownFile string `yaml:"known_file"`
	}
	if yaml.Unmarshal(data, &c) != nil || c.KnownFile == "" {
		return "", nil // config.Load says what is wrong
	}
	file := config.Path(filepath.Dir(path), c.KnownFile)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(f, "version: 1\nknown:\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(file)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(file)
		return "", err
	}
	return file, nil
}

// knownText is the entries as items of the known list, after a comment that
// says when they were added.
func knownText(entries []knownEntry, at time.Time) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(entries); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  # added by money-checks known add on %s\n", at.Format(time.DateOnly))
	for _, line := range strings.SplitAfter(buf.String(), "\n") {
		if line != "" {
			b.WriteString("  " + line)
		}
	}
	return b.String(), nil
}

func count(known map[string]map[string]config.Known) int {
	n := 0
	for _, ids := range known {
		n += len(ids)
	}
	return n
}

// writeAtomic replaces the file through a temporary file in the same directory,
// so a reader never sees half of it. The mode of an existing file is kept.
func writeAtomic(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

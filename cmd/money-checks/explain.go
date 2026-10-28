package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/report"
)

// explainChecks prints the plan summary of every query of the configuration. See
// docs/adr/0009-explain.md.
func explainChecks(ctx context.Context, args []string, stdout, stderr io.Writer, open check.Opener, now func() time.Time) int {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", "checks.yaml", "configuration file")
	window := fs.Bool("window", false, "checks with a window get the start of the window, as serve gives it")
	name := fs.String("check", "", "explain only this check")
	analyze := fs.Bool("analyze", false, "run the queries, with their timeout, and count the rows they read")
	jsonPath := fs.String("json", "", "also write the summaries and the plans to this JSON file")
	maxRows := fs.Int64("max-rows", 0, "exit with 1 when a plan reads more rows than this")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *maxRows < 0 {
		fmt.Fprintln(stderr, "explain: --max-rows must be 0 or more")
		return exitError
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if err := onlyCheck(&cfg, *name); err != nil {
		fmt.Fprintln(stderr, "explain:", err)
		return exitError
	}
	scope := check.Full
	if *window {
		scope = check.Window
	}
	r := check.Explain(ctx, cfg, scope, open, now, *analyze)
	r.Warnings = append(r.Warnings, cfg.Warnings...)
	for _, w := range r.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	if err := report.ExplainText(stdout, r, *maxRows); err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if *jsonPath != "" {
		if err := writePrivate(*jsonPath, func(w io.Writer) error { return report.ExplainJSON(w, r) }); err != nil {
			fmt.Fprintln(stderr, err)
			return exitError
		}
	}
	return r.ExitCode(*maxRows)
}

// onlyCheck keeps the check named name, when one is given.
func onlyCheck(cfg *config.Config, name string) error {
	if name == "" {
		return nil
	}
	var one []config.Check
	for _, ch := range cfg.Checks {
		if ch.Name == name {
			one = append(one, ch)
		}
	}
	if len(one) == 0 {
		return fmt.Errorf("no check is named %q", name)
	}
	cfg.Checks = one
	return nil
}

// writePrivate writes a file readable by its owner only, as the CSV of run: a
// plan can hold the values of a row.
func writePrivate(path string, write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".money-checks-*")
	if err != nil {
		return err
	}
	err = write(tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// validateChecks reads a configuration as run does, and what only a run used to
// read: every pipeline as MongoDB gets it and, with --env, the environment
// variables. Nothing connects, so it fits the CI of a repository of checks,
// which has neither the databases nor their secrets.
func validateChecks(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", "checks.yaml", "configuration file")
	withoutKnown := fs.Bool("without-known", false, "do not read the known file, which is kept apart from the checks")
	env := fs.Bool("env", false, "also require the environment variables of the sources and Slack")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	load := config.Load
	if *withoutKnown {
		load = config.LoadWithoutKnown
	}
	cfg, err := load(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	var errs []error
	// The name of an expect or a side says which query it is.
	pipeline := func(ch config.Check) {
		if cfg.Sources[ch.Source].Kind() != "mongodb" {
			return
		}
		if _, err := source.Pipeline(&ch.Pipeline); err != nil {
			errs = append(errs, fmt.Errorf("check %s: %w", ch.Name, err))
		}
	}
	for _, ch := range cfg.Checks {
		queries := []config.Check{ch}
		if ch.Compare != nil {
			queries = []config.Check{ch.Side("left"), ch.Side("right")}
		}
		for _, q := range queries {
			if q.Expect != nil {
				pipeline(q.ExpectCheck(cfg.Sources[q.Source].Kind()))
			}
			pipeline(q)
		}
	}
	if *env {
		for _, name := range slices.Sorted(maps.Keys(cfg.Sources)) {
			if _, err := config.Getenv(cfg.Sources[name].Env()); err != nil {
				errs = append(errs, fmt.Errorf("source %s: %w", name, err))
			}
		}
		if cfg.Serve.Slack != nil {
			if _, err := config.Getenv(cfg.Serve.Slack.WebhookEnv); err != nil {
				errs = append(errs, fmt.Errorf("serve.slack: %w", err))
			}
		}
	}
	if len(errs) > 0 {
		fmt.Fprintf(stderr, "%s: %v\n", *path, errors.Join(errs...))
		return exitError
	}
	warnings := cfg.Warnings
	if names := cfg.WithoutExpect(); len(names) > 0 {
		warnings = append(warnings, check.WithoutExpectWarning(names))
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	known := 0
	for _, ids := range cfg.Known {
		known += len(ids)
	}
	fmt.Fprintf(stdout, "%s: %s on %s, %s; nothing connected.\n", filepath.Base(*path),
		plural(int64(len(cfg.Checks)), "check"), plural(int64(len(cfg.Sources)), "source"),
		plural(int64(known), "known violation"))
	return exitOK
}

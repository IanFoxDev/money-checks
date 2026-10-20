// Command money-checks runs money invariants against an application's database and
// reports every row that breaks one.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/report"
	"github.com/ianfoxdev/money-checks/internal/serve"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

// Exit codes.
const (
	exitOK    = 0
	exitFound = 1
	exitError = 2
)

const usage = `usage:
  money-checks run -c checks.yaml [--window] [--markdown file] [--json file]
                   [--check NAME [--csv file] [--jsonl file]]
      run every check once; the Markdown report goes to stdout unless a file is given;
      with --window, checks that have a window read only its rows; with --check, only
      that check, and --csv or --jsonl write every one of its violations
  money-checks serve -c checks.yaml
      run the checks now and every serve.interval; /metrics, /healthz, /readyz on serve.listen
  money-checks known add -c checks.yaml --check NAME --reason TEXT [--until DATE] [--limit N]
      run one check and add its new violations to the known file
  money-checks test -c checks.yaml -t tests.yaml
      run the checks the tests file lists against a test database and compare what
      they find with what they must find; 0 all pass, 1 one does not, 2 a check failed
  money-checks version

exit codes of run: 0 no violations of severity error, 1 violations found, 2 a check failed`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, source.Open, time.Now)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, open check.Opener, now func() time.Time) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitError
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return exitOK
	case "run":
		return runChecks(ctx, args[1:], stdout, stderr, open, now)
	case "serve":
		return serveChecks(ctx, args[1:], stderr, open, now)
	case "test":
		return testChecks(ctx, args[1:], stdout, stderr, open, now)
	case "known":
		if len(args) > 1 && args[1] == "add" {
			return knownAdd(ctx, args[2:], stdout, stderr, open, now)
		}
		fmt.Fprintln(stderr, usage)
		return exitError
	default:
		fmt.Fprintln(stderr, usage)
		return exitError
	}
}

func runChecks(ctx context.Context, args []string, stdout, stderr io.Writer, open check.Opener, now func() time.Time) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", "checks.yaml", "configuration file")
	mdPath := fs.String("markdown", "", "write the Markdown report to this file instead of stdout")
	jsonPath := fs.String("json", "", "also write the JSON report to this file")
	window := fs.Bool("window", false, "checks with a window read only the rows of the window")
	name := fs.String("check", "", "run only this check")
	csvPath := fs.String("csv", "", "write every violation of the --check to this CSV file")
	jsonlPath := fs.String("jsonl", "", "write every violation of the --check to this JSON Lines file")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if (*csvPath != "" || *jsonlPath != "") && *name == "" {
		fmt.Fprintln(stderr, "run: --csv and --jsonl need --check: every row of one check, for one thing to do with them")
		return exitError
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if *name != "" {
		var one []config.Check
		for _, ch := range cfg.Checks {
			if ch.Name == *name {
				one = append(one, ch)
			}
		}
		if len(one) == 0 {
			fmt.Fprintf(stderr, "run: no check is named %q\n", *name)
			return exitError
		}
		cfg.Checks = one
	}
	scope := check.Full
	if *window {
		scope = check.Window
	}
	exp, err := newExport(cfg.Checks[0], *csvPath, *jsonlPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	var each func(string, check.Sample) error
	if len(exp.files) > 0 {
		each = func(_ string, s check.Sample) error { return exp.write(s) }
	}
	r := check.RunExport(ctx, cfg, scope, open, now, each)
	if len(exp.files) > 0 {
		// A failed check leaves no file: half a list looks like a whole one.
		if r.Results[0].Status == check.StatusFailed {
			exp.discard()
		} else if err := exp.finish(); err != nil {
			fmt.Fprintln(stderr, err)
			return exitError
		}
	}
	r.Warnings = append(r.Warnings, cfg.Warnings...)
	if names := cfg.WithoutExpect(); len(names) > 0 {
		r.Warnings = append(r.Warnings, check.WithoutExpectWarning(names))
	}
	for _, w := range r.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	if *mdPath == "" {
		if err := report.Markdown(stdout, r); err != nil {
			fmt.Fprintln(stderr, err)
			return exitError
		}
	} else if err := writeFile(*mdPath, r, report.Markdown); err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	if *jsonPath != "" {
		if err := writeFile(*jsonPath, r, report.JSON); err != nil {
			fmt.Fprintln(stderr, err)
			return exitError
		}
	}
	for _, res := range r.Results {
		if res.Status == check.StatusFailed {
			fmt.Fprintf(stderr, "check %s failed: %s\n", res.Name, res.Error)
		}
	}
	return r.ExitCode()
}

func serveChecks(ctx context.Context, args []string, stderr io.Writer, open check.Opener, now func() time.Time) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", "checks.yaml", "configuration file")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	var notify serve.Notifier
	if cfg.Serve.Slack != nil {
		url, err := config.Getenv(cfg.Serve.Slack.WebhookEnv)
		if err != nil {
			fmt.Fprintln(stderr, "serve.slack:", err)
			return exitError
		}
		notify = serve.NewSlack(url)
	}
	if err := serve.New(cfg, open, notify, log, now).Run(ctx); err != nil {
		log.Error("serve stopped", "error", err)
		return exitError
	}
	return exitOK
}

func writeFile(path string, r check.Report, write func(io.Writer, check.Report) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f, r); err != nil {
		_ = f.Close()
		return fmt.Errorf("%s: %w", path, err)
	}
	return f.Close()
}

// Command money-checks runs money invariants against an application's database and
// reports every row that breaks one.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/report"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// version is set at build time with -ldflags "-X main.version=v0.1.0".
var version = "dev"

// Exit codes.
const (
	exitOK    = 0
	exitError = 2
)

const usage = `usage:
  money-checks run -c checks.yaml [--markdown file] [--json file]
      run every check once; the Markdown report goes to stdout unless a file is given
  money-checks version

exit codes: 0 no violations of severity error, 1 violations found, 2 a check failed`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	r := check.Run(ctx, cfg, open, now)
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

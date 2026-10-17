package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/money"
)

// maxTestRows is the most rows a check may return in a test; a test database
// should be small. See docs/adr/0005-expect-and-test.md.
const maxTestRows = 10000

// testChecks runs the checks a tests file lists and compares what they find with
// what they must find. It only reads, like every command: the test database is
// filled by its migrations and seeds, not by money-checks.
func testChecks(ctx context.Context, args []string, stdout, stderr io.Writer, open check.Opener, now func() time.Time) int {
	fset := flag.NewFlagSet("test", flag.ContinueOnError)
	fset.SetOutput(stderr)
	path := fset.String("c", "checks.yaml", "configuration file")
	testsPath := fset.String("t", "tests.yaml", "tests file")
	if err := fset.Parse(args); err != nil {
		return exitError
	}
	// The known file is a fact about one production database; tests check the
	// queries.
	cfg, err := config.LoadWithoutKnown(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	tests, err := cfg.LoadTests(*testsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	want := map[string]config.Test{}
	for _, t := range tests {
		want[t.Check] = t
	}
	// Every row is a sample, up to one more than the limit, so too many is visible.
	n := maxTestRows + 1
	var checks []config.Check
	var untested []string
	for _, ch := range cfg.Checks {
		if _, ok := want[ch.Name]; !ok {
			untested = append(untested, ch.Name)
			continue
		}
		// A full run: a check with a window gets the start of a full pass as $1,
		// so its query is tested without depending on the clock.
		ch.Samples = &n
		checks = append(checks, ch)
	}
	cfg.Checks = checks
	r := check.Run(ctx, cfg, check.Full, open, now)
	for _, w := range r.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}

	passed, failed, broken := 0, 0, 0
	for _, res := range r.Results {
		switch {
		case res.Status == check.StatusFailed:
			broken++
			fmt.Fprintf(stdout, "ERROR %s: %s\n", res.Name, res.Error)
		case res.Violations > maxTestRows:
			broken++
			fmt.Fprintf(stdout, "ERROR %s: more than %d rows; a test database should be small\n", res.Name, maxTestRows)
		default:
			diff := compareTest(cfg.Money, want[res.Name], res)
			if len(diff) == 0 {
				passed++
				fmt.Fprintf(stdout, "ok    %s: %s\n", res.Name, plural(res.Violations, "violation"))
				continue
			}
			failed++
			fmt.Fprintf(stdout, "FAIL  %s: %s, want %s\n", res.Name, plural(res.Violations, "violation"), plural(int64(len(want[res.Name].Violations)), "violation"))
			for _, d := range diff {
				fmt.Fprintf(stdout, "        %s\n", d)
			}
		}
	}
	if len(untested) > 0 {
		fmt.Fprintf(stdout, "\nnot tested: %s\n", strings.Join(untested, ", "))
	}
	fmt.Fprintf(stdout, "\n%s: %d passed, %d failed, %d could not run\n", plural(int64(len(r.Results)), "test"), passed, failed, broken)
	switch {
	case broken > 0:
		return exitError
	case failed > 0:
		return exitFound
	}
	return exitOK
}

// compareTest lists the differences between what a check found and what its
// test wants, sorted by id: a row that is missing, one that should not be there,
// an amount that differs.
func compareTest(cur money.Currencies, t config.Test, res check.Result) []string {
	got := map[string]check.Sample{}
	for _, s := range res.Samples {
		got[s.ID] = s
	}
	type line struct{ id, text string }
	var lines []line
	for _, v := range t.Violations {
		s, ok := got[v.ID]
		delete(got, v.ID)
		switch {
		case !ok:
			text := "missing " + v.ID
			if v.Amount != nil {
				text += " " + testAmount(cur, *v.Amount)
			}
			lines = append(lines, line{v.ID, text})
		case v.Amount != nil && (s.Amount == nil || s.Amount.Currency != v.Amount.Currency || s.Amount.Minor != v.Amount.Minor):
			lines = append(lines, line{v.ID, fmt.Sprintf("amount  %s %s, want %s", v.ID, foundAmount(s), testAmount(cur, *v.Amount))})
		}
	}
	for id, s := range got {
		text := "extra   " + id
		if s.Amount != nil {
			text += " " + s.Amount.String()
		}
		lines = append(lines, line{id, text})
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].id < lines[j].id })
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.text
	}
	return out
}

func foundAmount(s check.Sample) string {
	if s.Amount == nil {
		return "none"
	}
	return s.Amount.String()
}

// testAmount writes an amount of a tests file the way the report writes amounts.
func testAmount(cur money.Currencies, a config.KnownAmount) string {
	exp, _ := cur.Exponent(a.Currency)
	return money.FormatMinor(a.Minor, exp) + " " + a.Currency
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/config"
)

// diffChecks runs the old and the new version of every check that differs
// between two configurations against the same database, and prints the rows
// only one of them finds and the rows whose amount differs: what a change to a
// check adds and what it hides, for its review. The known file is left out: it
// accepts rows, it does not change what a check finds. See
// docs/adr/0010-violation-history.md.
func diffChecks(ctx context.Context, args []string, stdout, stderr io.Writer, open check.Opener, now func() time.Time) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("c", "checks.yaml", "the configuration with the new version of the checks")
	oldPath := fs.String("old", "", "the configuration with the old version of the checks")
	name := fs.String("check", "", "only this check, also when it did not change")
	window := fs.Bool("window", false, "checks with a window read only the rows of the window")
	limit := fs.Int("limit", 50, "the most ids printed per list; 0 prints every id")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *oldPath == "" || fs.NArg() > 0 || *limit < 0 {
		fmt.Fprintln(stderr, "diff: --old FILE is required")
		return exitError
	}
	newCfg, err := config.LoadWithoutKnown(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}
	oldCfg, err := config.LoadWithoutKnown(*oldPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitError
	}

	type pair struct {
		name       string
		old, new   *config.Check
		oldV, newV string
	}
	var pairs []pair
	olds := map[string]*config.Check{}
	for i := range oldCfg.Checks {
		olds[oldCfg.Checks[i].Name] = &oldCfg.Checks[i]
	}
	seen := map[string]bool{}
	for i := range newCfg.Checks {
		ch := &newCfg.Checks[i]
		seen[ch.Name] = true
		p := pair{name: ch.Name, new: ch, newV: newCfg.CheckVersion(*ch), old: olds[ch.Name]}
		if p.old != nil {
			p.oldV = oldCfg.CheckVersion(*p.old)
		}
		if *name == ch.Name || *name == "" && p.oldV != p.newV {
			pairs = append(pairs, p)
		}
	}
	for i := range oldCfg.Checks {
		ch := &oldCfg.Checks[i]
		if !seen[ch.Name] && (*name == "" || *name == ch.Name) {
			pairs = append(pairs, pair{name: ch.Name, old: ch, oldV: oldCfg.CheckVersion(*ch)})
		}
	}
	if *name != "" && len(pairs) == 0 {
		fmt.Fprintf(stderr, "diff: no check is named %q\n", *name)
		return exitError
	}
	if len(pairs) == 0 {
		fmt.Fprintln(stdout, "No check differs between the two configurations.")
		return exitOK
	}

	scope := check.Full
	if *window {
		scope = check.Window
	}
	// Both versions get the same time, so a window is the same window.
	at := now()
	clock := func() time.Time { return at }
	run := func(cfg config.Config, checks []config.Check) (map[string]map[string]*check.Money, map[string]check.Result) {
		cfg.Checks = checks
		rows := map[string]map[string]*check.Money{}
		r := check.RunExport(ctx, cfg, scope, open, clock, func(name string, s check.Sample) error {
			if rows[name] == nil {
				rows[name] = map[string]*check.Money{}
			}
			rows[name][s.ID] = s.Amount
			return nil
		})
		results := map[string]check.Result{}
		for _, res := range r.Results {
			results[res.Name] = res
		}
		return rows, results
	}
	var oldChecks, newChecks []config.Check
	for _, p := range pairs {
		if p.old != nil {
			oldChecks = append(oldChecks, *p.old)
		}
		if p.new != nil {
			newChecks = append(newChecks, *p.new)
		}
	}
	oldRows, oldRes := run(oldCfg, oldChecks)
	newRows, newRes := run(newCfg, newChecks)

	code := exitOK
	for i, p := range pairs {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		failed := false
		for _, res := range []check.Result{oldRes[p.name], newRes[p.name]} {
			if res.Status == check.StatusFailed {
				fmt.Fprintf(stdout, "%s: failed: %s\n", p.name, res.Error)
				failed = true
			}
		}
		if failed {
			code = exitError
			continue
		}
		o, n := oldRows[p.name], newRows[p.name]
		var onlyOld, onlyNew, amount []string
		for id, a := range o {
			b, ok := n[id]
			switch {
			case !ok:
				onlyOld = append(onlyOld, id+amountOf(a))
			case !sameMoney(a, b):
				amount = append(amount, id+amountOf(a)+" ->"+amountOf(b))
			}
		}
		for id, b := range n {
			if _, ok := o[id]; !ok {
				onlyNew = append(onlyNew, id+amountOf(b))
			}
		}
		switch {
		case p.old == nil:
			fmt.Fprintf(stdout, "%s: new check, version %s: %s\n", p.name, p.newV, plural(int64(len(n)), "row"))
		case p.new == nil:
			fmt.Fprintf(stdout, "%s: removed, version %s found %s\n", p.name, p.oldV, plural(int64(len(o)), "row"))
		default:
			fmt.Fprintf(stdout, "%s: version %s -> %s: %s before, %s now; %d only old, %d only new, %d with another amount\n",
				p.name, p.oldV, p.newV, plural(int64(len(o)), "row"), plural(int64(len(n)), "row"), len(onlyOld), len(onlyNew), len(amount))
		}
		list(stdout, "only old (the new version hides them)", onlyOld, *limit)
		list(stdout, "only new", onlyNew, *limit)
		list(stdout, "another amount", amount, *limit)
		if len(onlyOld)+len(onlyNew)+len(amount) > 0 && code == exitOK {
			code = exitFound
		}
	}
	return code
}

// list prints the ids of one kind of difference, sorted, at most limit of them.
func list(w io.Writer, title string, ids []string, limit int) {
	if len(ids) == 0 {
		return
	}
	slices.Sort(ids)
	fmt.Fprintf(w, "  %s:\n", title)
	for i, id := range ids {
		if limit > 0 && i == limit {
			fmt.Fprintf(w, "    and %d more\n", len(ids)-limit)
			return
		}
		fmt.Fprintf(w, "    %s\n", id)
	}
}

func amountOf(m *check.Money) string {
	if m == nil {
		return ""
	}
	return " " + m.String()
}

func sameMoney(a, b *check.Money) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Currency == b.Currency && a.Minor == b.Minor
}

package check

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/source"
)

// total is what one side returned for a key.
type total struct {
	amount *Money
	counts []int64
}

// runCompare reads the left side into memory, streams the right side past it and
// counts every key where the two differ; see docs/adr/0004-compare.md. The
// violations are sorted by key, so the samples do not depend on the order the
// databases return rows in.
func runCompare(ctx context.Context, ch config.Check, get func(string) (source.Source, error), c *collector) error {
	cmp := ch.Compare
	c.abs = true
	left := map[string]total{}
	err := readSide(ctx, ch, "left", get, c, func(key string, t total) error {
		if _, dup := left[key]; dup {
			return fmt.Errorf("key %s is returned twice; group the rows by the key", key)
		}
		left[key] = t
		return nil
	})
	if err != nil {
		return err
	}
	var found []Sample
	seen := map[string]bool{}
	err = readSide(ctx, ch, "right", get, c, func(key string, r total) error {
		if seen[key] {
			return fmt.Errorf("key %s is returned twice; group the rows by the key", key)
		}
		seen[key] = true
		l, ok := left[key]
		var lp *total
		if ok {
			lp = &l
		}
		s, bad, err := differ(cmp, key, lp, &r)
		if bad {
			found = append(found, s)
		}
		return err
	})
	if err != nil {
		return err
	}
	for key, l := range left {
		if seen[key] {
			continue
		}
		s, _, err := differ(cmp, key, &l, nil)
		if err != nil {
			return err
		}
		found = append(found, s)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
	for _, s := range found {
		fields := s.Fields
		if err := c.add(s, func() []Field { return fields }); err != nil {
			return err
		}
	}
	return nil
}

// readSide runs one side and calls each with the key and the total of every row.
// Its errors say which side they come from.
func readSide(ctx context.Context, ch config.Check, name string, get func(string) (source.Source, error), c *collector, each func(string, total) error) error {
	cmp := ch.Compare
	side := ch.Side(name)
	src, err := get(side.Source)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	rows := 0
	err = src.Query(ctx, side, c.res.Since, c.res.Until, func(row source.Row) error {
		rows++
		if rows > cmp.MaxRows {
			return fmt.Errorf("more than max_rows (%d) rows; a side returns one row per key, grouped in the query", cmp.MaxRows)
		}
		parts := make([]string, len(cmp.Key))
		for i, k := range cmp.Key {
			v, ok := row.Get(k)
			if !ok {
				return fmt.Errorf("row %d has no %s column (the key); the side returns %s", rows, k, strings.Join(row.Columns, ", "))
			}
			parts[i] = Display(v)
		}
		key := strings.Join(parts, "/")
		var t total
		if cmp.Amount != nil {
			m, err := amount(c.cur, *cmp.Amount, row)
			if err != nil {
				return fmt.Errorf("key %s: %w", key, err)
			}
			t.amount = &m
		}
		for _, name := range cmp.Counts {
			v, ok := row.Get(name)
			if !ok {
				return fmt.Errorf("key %s: no %s column (a count)", key, name)
			}
			n, err := integer(v)
			if err != nil {
				return fmt.Errorf("key %s: %s = %s: %w", key, name, Display(v), err)
			}
			t.counts = append(t.counts, n)
		}
		return each(key, t)
	})
	if err != nil {
		return fmt.Errorf("%s: %s", name, errorText(err, side))
	}
	return nil
}

// differ compares the totals of one key; l or r is nil when only the other side
// returned the key. bad says whether that is a violation. The sample has the
// difference, right minus left, as its amount, and both sides in its fields:
// left, right, then each count on the left and on the right, then what differs.
func differ(cmp *config.Compare, key string, l, r *total) (Sample, bool, error) {
	s := Sample{ID: key}
	var problems []string
	switch {
	case l == nil:
		problems = append(problems, "only right")
	case r == nil:
		problems = append(problems, "only left")
	}
	side := func(t *total) string {
		if t == nil || t.amount == nil {
			return "-"
		}
		return t.amount.String()
	}
	if cmp.Amount != nil {
		var lm, rm int64
		m := Money{}
		for _, t := range []*total{l, r} {
			if t != nil {
				m.Currency, m.Exponent = t.amount.Currency, t.amount.Exponent
			}
		}
		if l != nil {
			lm = l.amount.Minor
		}
		if r != nil {
			rm = r.amount.Minor
		}
		if l != nil && r != nil && l.amount.Currency != r.amount.Currency {
			// The configuration keeps the currency in the key, so this is a
			// fixed currency the two sides cannot disagree on.
			return s, false, fmt.Errorf("key %s: the sides are in %s and %s", key, l.amount.Currency, r.amount.Currency)
		}
		diff, ok := sub(rm, lm)
		if !ok {
			return s, false, fmt.Errorf("key %s: the difference does not fit in int64 minor units", key)
		}
		m.Minor = diff
		s.Amount = &m
		if l != nil && r != nil && (diff > cmp.ToleranceMinor || diff < -cmp.ToleranceMinor) {
			problems = append(problems, "amount differs")
		}
		s.Fields = append(s.Fields, Field{Name: "left", Value: side(l)}, Field{Name: "right", Value: side(r)})
	}
	for i, name := range cmp.Counts {
		lv, rv := "-", "-"
		if l != nil {
			lv = strconv.FormatInt(l.counts[i], 10)
		}
		if r != nil {
			rv = strconv.FormatInt(r.counts[i], 10)
		}
		if l != nil && r != nil && l.counts[i] != r.counts[i] {
			problems = append(problems, name+" differs")
		}
		s.Fields = append(s.Fields, Field{Name: name + " left", Value: lv}, Field{Name: name + " right", Value: rv})
	}
	s.Fields = append(s.Fields, Field{Name: "problem", Value: strings.Join(problems, ", ")})
	return s, len(problems) > 0, nil
}

// sub is a - b and whether it fits in int64.
func sub(a, b int64) (int64, bool) {
	d := a - b
	overflow := b > 0 && d > a || b < 0 && d < a
	return d, !overflow
}

// integer reads a count: an integer, its text, or a double with no fraction.
func integer(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int32:
		return int64(x), nil
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0, errors.New("not an integer")
		}
		return n, nil
	case float64:
		if x != math.Trunc(x) || math.Abs(x) >= 1<<53 {
			return 0, errors.New("not an integer")
		}
		return int64(x), nil
	default:
		return 0, errors.New("not an integer")
	}
}

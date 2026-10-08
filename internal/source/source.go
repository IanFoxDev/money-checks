// Package source runs a check's query against a database and hands back the rows.
// Every source only reads; see docs/adr/0001-read-only-and-money.md.
package source

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/ianfoxdev/money-checks/internal/config"
	"github.com/ianfoxdev/money-checks/internal/money"
)

// Row is one violation as the database returned it. A value is nil, string, bool,
// int64, float64, Decimal, or a type the source documents.
type Row struct {
	Columns []string
	Values  []any
}

// Get returns the value of a column and whether the row has it.
func (r Row) Get(column string) (any, bool) {
	for i, c := range r.Columns {
		if c == column {
			return r.Values[i], true
		}
	}
	return nil, false
}

// Decimal is Coef * 10^Scale, as MongoDB Decimal128 and SQL numeric store it.
type Decimal struct {
	Coef  *big.Int
	Scale int
}

// String writes the value as a plain decimal: 1099e-2 is "10.99", 11e2 is "1100".
func (d Decimal) String() string {
	s := new(big.Int).Abs(d.Coef).String()
	sign := ""
	if d.Coef.Sign() < 0 {
		sign = "-"
	}
	switch {
	case d.Scale >= 0:
		if d.Coef.Sign() != 0 {
			s += strings.Repeat("0", d.Scale)
		}
	case len(s) <= -d.Scale:
		s = "0." + strings.Repeat("0", -d.Scale-len(s)) + s
	default:
		s = s[:len(s)+d.Scale] + "." + s[len(s)+d.Scale:]
	}
	return sign + s
}

// Source runs checks against one database.
type Source interface {
	// Query runs the check and calls each for every row, in the order the database
	// returns them. It stops at the first error each returns.
	Query(ctx context.Context, ch config.Check, each func(Row) error) error
	Close(ctx context.Context) error
}

// Open connects to a source and checks that it can only read. warn gets messages
// that do not stop the run, such as a MongoDB user that can write when
// require_read_only is false.
func Open(ctx context.Context, name string, s config.Source, warn func(string)) (Source, error) {
	switch {
	case s.MongoDB != nil:
		src, err := openMongoDB(ctx, *s.MongoDB, func(msg string) { warn("source " + name + ": " + msg) })
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", name, err)
		}
		return src, nil
	case s.Postgres != nil:
		src, err := openPostgres(ctx, *s.Postgres)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", name, err)
		}
		return src, nil
	default:
		return nil, fmt.Errorf("source %s: set mongodb or postgres", name)
	}
}

// ErrAmount is wrapped by every error about an amount value.
var ErrAmount = errors.New("bad amount")

// Minor turns an amount value into minor units of a currency with exp decimal
// places. unit is "minor" (the value counts minor units) or "major".
func Minor(v any, unit string, exp int) (int64, error) {
	if unit == "minor" {
		exp = 0
	}
	var (
		n   int64
		err error
	)
	switch x := v.(type) {
	case nil:
		return 0, fmt.Errorf("%w: empty", ErrAmount)
	case int64:
		n, err = money.FromMajor(x, exp)
	case int32:
		n, err = money.FromMajor(int64(x), exp)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("%w: %v", ErrAmount, x)
		}
		n, err = money.FromFloat(x, exp)
	case string:
		n, err = money.Parse(x, exp, money.Format{})
	case Decimal:
		n, err = money.FromScaled(x.Coef, x.Scale, exp)
	default:
		return 0, fmt.Errorf("%w: %T is not a number", ErrAmount, v)
	}
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrAmount, err)
	}
	return n, nil
}

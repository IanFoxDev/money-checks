package report

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"

	"github.com/ianfoxdev/money-checks/internal/check"
	"github.com/ianfoxdev/money-checks/internal/money"
)

// CSV writes every violation of one check as a row, for a spreadsheet: id,
// amount and currency when the check has an amount, the columns of its samples,
// then known (the reason, for a known violation) and known_note (why a row the
// known file lists counts again). The header is written first. Call Flush at
// the end.
type CSV struct {
	w         *csv.Writer
	columns   []string
	amount    bool
	headerErr error
}

// NewCSV starts a CSV with the header for these columns, as check.Columns names
// them.
func NewCSV(w io.Writer, columns []string, amount bool) *CSV {
	c := &CSV{w: csv.NewWriter(w), columns: columns, amount: amount}
	header := []string{"id"}
	if amount {
		header = append(header, "amount", "currency")
	}
	header = append(header, columns...)
	c.headerErr = c.w.Write(append(header, "known", "known_note"))
	return c
}

// Write adds one violation.
func (c *CSV) Write(s check.Sample) error {
	if c.headerErr != nil {
		return c.headerErr
	}
	rec := []string{text(s.ID)}
	if c.amount {
		if s.Amount != nil {
			rec = append(rec, money.FormatMinor(s.Amount.Minor, s.Amount.Exponent), s.Amount.Currency)
		} else {
			rec = append(rec, "", "")
		}
	}
	for i := range c.columns {
		v := ""
		if i < len(s.Fields) {
			v = s.Fields[i].Value
		}
		rec = append(rec, text(v))
	}
	return c.w.Write(append(rec, text(s.Reason), text(s.Note)))
}

// Flush writes what is buffered and returns the first error.
func (c *CSV) Flush() error {
	c.w.Flush()
	if c.headerErr != nil {
		return c.headerErr
	}
	return c.w.Error()
}

// text keeps a value from the database from running as a formula when the file
// is opened in a spreadsheet: "=HYPERLINK(...)" becomes "'=HYPERLINK(...)". A
// number such as -30.00 and the "-" of a compare side without the key are left
// as they are.
func text(v string) string {
	if v == "" || v == "-" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "'" + v
		}
	}
	return v
}

// JSONLines writes every violation of one check as one JSON object per line, in
// the form of the samples of the JSON report.
type JSONLines struct {
	enc *json.Encoder
}

// NewJSONLines starts JSON Lines on w.
func NewJSONLines(w io.Writer) *JSONLines {
	return &JSONLines{enc: json.NewEncoder(w)}
}

// Write adds one violation.
func (j *JSONLines) Write(s check.Sample) error {
	return j.enc.Encode(toJSONSample(s))
}

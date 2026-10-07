// Package money parses and formats amounts as int64 minor units, without floats.
// The code comes from github.com/ianfoxdev/recon. See docs/adr/0001-read-only-and-money.md.
package money

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Currencies knows the number of minor-unit digits of each currency.
type Currencies struct {
	overrides map[string]int
}

// NewCurrencies uses the ISO 4217 table, with overrides for codes it does not know
// or that a provider treats differently.
func NewCurrencies(overrides map[string]int) (Currencies, error) {
	o := make(map[string]int, len(overrides))
	for code, exp := range overrides {
		if exp < 0 || exp > 8 {
			return Currencies{}, fmt.Errorf("currency %s: exponent %d out of range 0..8", code, exp)
		}
		o[strings.ToUpper(code)] = exp
	}
	return Currencies{overrides: o}, nil
}

// Exponent is the number of minor-unit digits of a currency.
func (c Currencies) Exponent(code string) (int, bool) {
	code = strings.ToUpper(code)
	if exp, ok := c.overrides[code]; ok {
		return exp, true
	}
	exp, ok := iso4217[code]
	return exp, ok
}

// Format says how a decimal string is written.
type Format struct {
	Decimal   rune // '.' if zero
	Thousands rune // none if zero
}

// ErrSyntax is wrapped by every parse error.
var ErrSyntax = errors.New("invalid amount")

// Parse turns a decimal string in major units ("1,234.50", "-10.99", "100") into
// minor units for a currency with exp minor-unit digits. Extra decimal places are
// accepted only when they are zeros ("100.00" for JPY); anything else is an error,
// never a rounding.
func Parse(s string, exp int, f Format) (int64, error) {
	dec := f.Decimal
	if dec == 0 {
		dec = '.'
	}
	in := strings.TrimSpace(s)
	if in == "" {
		return 0, fmt.Errorf("%w: empty", ErrSyntax)
	}
	negative := false
	switch in[0] {
	case '-':
		negative = true
		in = in[1:]
	case '+':
		in = in[1:]
	}
	whole, frac, hasFrac := strings.Cut(in, string(dec))
	if hasFrac && frac == "" || whole == "" && !hasFrac {
		return 0, fmt.Errorf("%w: %q", ErrSyntax, s)
	}
	digits, err := wholeDigits(whole, f.Thousands)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", ErrSyntax, s, err)
	}
	for i, r := range frac {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%w: %q: unexpected %q in the decimal part", ErrSyntax, s, frac[i])
		}
	}
	if len(frac) > exp {
		if strings.Trim(frac[exp:], "0") != "" {
			return 0, fmt.Errorf("%w: %q has %d decimal places, the currency has %d", ErrSyntax, s, len(frac), exp)
		}
		frac = frac[:exp]
	}
	frac += strings.Repeat("0", exp-len(frac))

	var minor int64
	for _, r := range digits + frac {
		d := int64(r - '0')
		if minor > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("%w: %q does not fit in int64 minor units", ErrSyntax, s)
		}
		minor = minor*10 + d
	}
	if negative {
		minor = -minor
	}
	return minor, nil
}

// wholeDigits checks the integer part and drops thousands separators, which must
// group digits by three.
func wholeDigits(whole string, thousands rune) (string, error) {
	if whole == "" {
		return "0", nil
	}
	if thousands != 0 && strings.ContainsRune(whole, thousands) {
		groups := strings.Split(whole, string(thousands))
		for i, g := range groups {
			if g == "" || len(g) > 3 || i > 0 && len(g) != 3 {
				return "", errors.New("thousands separators must group digits by three")
			}
		}
		whole = strings.Join(groups, "")
	}
	for i := 0; i < len(whole); i++ {
		if whole[i] < '0' || whole[i] > '9' {
			return "", fmt.Errorf("unexpected %q", whole[i])
		}
	}
	return whole, nil
}

// FormatMinor writes minor units as a plain decimal string: 1099 with exp 2 is "10.99".
func FormatMinor(minor int64, exp int) string {
	sign := ""
	u := uint64(minor)
	if minor < 0 {
		sign = "-"
		u = uint64(-(minor + 1)) + 1 // safe for math.MinInt64
	}
	s := fmt.Sprintf("%d", u)
	if exp == 0 {
		return sign + s
	}
	if len(s) <= exp {
		s = strings.Repeat("0", exp-len(s)+1) + s
	}
	return sign + s[:len(s)-exp] + "." + s[len(s)-exp:]
}

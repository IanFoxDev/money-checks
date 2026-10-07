package money

import (
	"errors"
	"math"
	"testing"
)

func TestParse(t *testing.T) {
	us := Format{Decimal: '.', Thousands: ','}
	eu := Format{Decimal: ',', Thousands: '.'}
	tests := []struct {
		in   string
		exp  int
		f    Format
		want int64
	}{
		{"10.99", 2, Format{}, 1099},
		{"10.9", 2, Format{}, 1090},
		{"10", 2, Format{}, 1000},
		{".5", 2, Format{}, 50},
		{"-10.99", 2, Format{}, -1099},
		{"+3.00", 2, Format{}, 300},
		{"  7.25 ", 2, Format{}, 725},
		{"1,234,567.89", 2, us, 123456789},
		{"1.234.567,89", 2, eu, 123456789},
		{"1100", 0, Format{}, 1100},
		{"100.00", 0, Format{}, 100},  // extra zeros, as some exports write zero-decimal currencies
		{"10.990", 2, Format{}, 1099}, // extra zero
		{"1.234", 3, Format{}, 1234},  // KWD
		{"0.0001", 4, Format{}, 1},    // CLF
		{"0", 2, Format{}, 0},
		{"-0.01", 2, Format{}, -1},
		{"92233720368547758.07", 2, Format{}, math.MaxInt64},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in, tt.exp, tt.f)
		if err != nil || got != tt.want {
			t.Errorf("Parse(%q, %d) = %d, %v; want %d", tt.in, tt.exp, got, err, tt.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	us := Format{Decimal: '.', Thousands: ','}
	tests := []struct {
		in  string
		exp int
		f   Format
	}{
		{"", 2, Format{}},
		{"-", 2, Format{}},
		{"10.", 2, Format{}},
		{"10.999", 2, Format{}},  // a third decimal place in EUR is not rounded
		{"1.5", 0, Format{}},     // JPY has no minor units
		{"1,234.5", 2, Format{}}, // thousands separator not configured
		{"12,34.5", 2, us},       // groups of three
		{"1,2345.5", 2, us},
		{"1e3", 2, Format{}},
		{"10.9a", 2, Format{}},
		{"--1", 2, Format{}},
		{"92233720368547758.08", 2, Format{}}, // one past MaxInt64
	}
	for _, tt := range tests {
		if got, err := Parse(tt.in, tt.exp, tt.f); !errors.Is(err, ErrSyntax) {
			t.Errorf("Parse(%q, %d) = %d, %v; want ErrSyntax", tt.in, tt.exp, got, err)
		}
	}
}

func TestFormatMinor(t *testing.T) {
	tests := []struct {
		minor int64
		exp   int
		want  string
	}{
		{1099, 2, "10.99"},
		{5, 2, "0.05"},
		{-5, 2, "-0.05"},
		{1100, 0, "1100"},
		{1234, 3, "1.234"},
		{0, 2, "0.00"},
		{math.MinInt64, 2, "-92233720368547758.08"},
	}
	for _, tt := range tests {
		if got := FormatMinor(tt.minor, tt.exp); got != tt.want {
			t.Errorf("FormatMinor(%d, %d) = %q; want %q", tt.minor, tt.exp, got, tt.want)
		}
	}
}

func TestExponents(t *testing.T) {
	c, err := NewCurrencies(map[string]int{"usdt": 6})
	if err != nil {
		t.Fatal(err)
	}
	for code, want := range map[string]int{"EUR": 2, "jpy": 0, "KWD": 3, "CLF": 4, "USDT": 6} {
		if got, ok := c.Exponent(code); !ok || got != want {
			t.Errorf("Exponent(%s) = %d, %v; want %d", code, got, ok, want)
		}
	}
	if _, ok := c.Exponent("XAU"); ok {
		t.Error("gold has no minor units in ISO 4217 and must not be known")
	}
	if _, err := NewCurrencies(map[string]int{"BAD": 9}); err == nil {
		t.Error("exponent 9 accepted")
	}
	if iso4217Published == "" {
		t.Error("table without a publication date")
	}
}

// Formatting minor units and parsing the result gives the same number back.
func FuzzRoundTrip(f *testing.F) {
	for _, seed := range []int64{0, 1, -1, 1099, math.MaxInt64, math.MinInt64 + 1} {
		f.Add(seed, uint8(2))
	}
	f.Fuzz(func(t *testing.T, minor int64, e uint8) {
		exp := int(e % 5)
		if minor == math.MinInt64 {
			return // its absolute value has no int64; Parse rejects it on purpose
		}
		s := FormatMinor(minor, exp)
		got, err := Parse(s, exp, Format{})
		if err != nil || got != minor {
			t.Fatalf("Parse(FormatMinor(%d, %d) = %q) = %d, %v", minor, exp, s, got, err)
		}
	})
}

// Parse never panics, whatever the input.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{"10.99", "1,234.5", "-", "", "1e3", "9999999999999999999"} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, s string) {
		_, _ = Parse(s, 2, Format{Decimal: '.', Thousands: ','})
	})
}

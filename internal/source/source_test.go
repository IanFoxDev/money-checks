package source

import (
	"errors"
	"math/big"
	"testing"
)

func TestMinor(t *testing.T) {
	tests := []struct {
		v    any
		unit string
		exp  int
		want int64
	}{
		{int64(1099), "minor", 2, 1099},
		{int32(27), "major", 2, 2700},
		{"1099", "minor", 2, 1099},
		{"10.99", "major", 2, 1099},
		{"-0.01", "major", 2, -1},
		{10.1, "major", 2, 1010},
		{1099.0, "minor", 2, 1099},
		{Decimal{Coef: big.NewInt(1099), Scale: -2}, "major", 2, 1099},
		{Decimal{Coef: big.NewInt(1234), Scale: -3}, "major", 3, 1234},
		{Decimal{Coef: big.NewInt(11), Scale: 2}, "minor", 2, 1100},
	}
	for _, tt := range tests {
		got, err := Minor(tt.v, tt.unit, tt.exp)
		if err != nil || got != tt.want {
			t.Errorf("Minor(%#v, %s, %d) = %d, %v; want %d", tt.v, tt.unit, tt.exp, got, err, tt.want)
		}
	}
}

func TestMinorRejects(t *testing.T) {
	tests := []struct {
		v    any
		unit string
	}{
		{nil, "minor"},
		{"10.99", "minor"}, // a fraction of a minor unit
		{1099.5, "minor"},
		{10.105, "major"},
		{"ten", "major"},
		{true, "major"},
		{Decimal{Coef: big.NewInt(10105), Scale: -3}, "major"},
	}
	for _, tt := range tests {
		if got, err := Minor(tt.v, tt.unit, 2); !errors.Is(err, ErrAmount) {
			t.Errorf("Minor(%#v, %s) = %d, %v; want ErrAmount", tt.v, tt.unit, got, err)
		}
	}
}

func TestRowGet(t *testing.T) {
	r := Row{Columns: []string{"id", "amount"}, Values: []any{"p1", nil}}
	if v, ok := r.Get("id"); !ok || v != "p1" {
		t.Errorf("Get(id) = %v, %v", v, ok)
	}
	if v, ok := r.Get("amount"); !ok || v != nil {
		t.Errorf("Get(amount) = %v, %v", v, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("Get(missing) found a column")
	}
}

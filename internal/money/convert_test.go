package money

import (
	"errors"
	"math"
	"math/big"
	"testing"
)

func TestFromFloat(t *testing.T) {
	tests := []struct {
		in   float64
		exp  int
		want int64
	}{
		{10.1, 2, 1010},
		{26.99, 2, 2699},
		{0.29, 2, 29},
		{-0.01, 2, -1},
		{1100, 0, 1100},
		{1.234, 3, 1234},
		{math.Copysign(0, -1), 2, 0},
		{70368744177.64, 2, 7036874417764},
	}
	for _, tt := range tests {
		got, err := FromFloat(tt.in, tt.exp)
		if err != nil || got != tt.want {
			t.Errorf("FromFloat(%v, %d) = %d, %v; want %d", tt.in, tt.exp, got, err, tt.want)
		}
	}
}

func TestFromFloatRejects(t *testing.T) {
	a, b := 0.1, 0.2 // variables: Go adds constants exactly
	for _, v := range []float64{a + b, 10.105, 1.5, math.NaN(), math.Inf(1), 1e30} {
		exp := 2
		if v == 1.5 {
			exp = 0
		}
		if got, err := FromFloat(v, exp); !errors.Is(err, ErrSyntax) {
			t.Errorf("FromFloat(%v, %d) = %d, %v; want ErrSyntax", v, exp, got, err)
		}
	}
}

func TestFromScaled(t *testing.T) {
	tests := []struct {
		coef       int64
		scale, exp int
		want       int64
	}{
		{1099, -2, 2, 1099},  // 10.99
		{10990, -3, 2, 1099}, // 10.990
		{11, 2, 2, 110000},   // 1.1E+3
		{5, 0, 0, 5},
		{-1, -2, 2, -1},
		{0, 6000, 2, 0},
		{0, -6000, 2, 0},
		{1234, -3, 3, 1234}, // KWD
	}
	for _, tt := range tests {
		got, err := FromScaled(big.NewInt(tt.coef), tt.scale, tt.exp)
		if err != nil || got != tt.want {
			t.Errorf("FromScaled(%d, %d, %d) = %d, %v; want %d", tt.coef, tt.scale, tt.exp, got, err, tt.want)
		}
	}
}

func TestFromScaledRejects(t *testing.T) {
	huge, _ := new(big.Int).SetString("100000000000000000000", 10)
	tests := []struct {
		coef       *big.Int
		scale, exp int
	}{
		{big.NewInt(10105), -3, 2}, // 10.105 USD
		{big.NewInt(1), 18, 2},
		{big.NewInt(1), 6000, 2},
		{huge, 0, 0},
	}
	for _, tt := range tests {
		if got, err := FromScaled(tt.coef, tt.scale, tt.exp); !errors.Is(err, ErrSyntax) {
			t.Errorf("FromScaled(%s, %d, %d) = %d, %v; want ErrSyntax", tt.coef, tt.scale, tt.exp, got, err)
		}
	}
}

func TestFromMajor(t *testing.T) {
	if got, err := FromMajor(27, 2); err != nil || got != 2700 {
		t.Errorf("FromMajor(27, 2) = %d, %v", got, err)
	}
	if _, err := FromMajor(math.MaxInt64, 2); !errors.Is(err, ErrSyntax) {
		t.Errorf("FromMajor(MaxInt64, 2): %v, want ErrSyntax", err)
	}
}

// A whole number of cents stored as a double, the way PHP and JavaScript write
// prices into MongoDB, comes back exactly while doubles are finer than a cent.
func FuzzFromFloatCents(f *testing.F) {
	for _, m := range []int64{0, 1, 29, 2699, -1, 999999999999999} {
		f.Add(m)
	}
	f.Fuzz(func(t *testing.T, m int64) {
		if m > 1e15 || m < -1e15 {
			t.Skip()
		}
		got, err := FromFloat(float64(m)/100, 2)
		if err != nil || got != m {
			t.Fatalf("FromFloat(%d/100) = %d, %v", m, got, err)
		}
	})
}

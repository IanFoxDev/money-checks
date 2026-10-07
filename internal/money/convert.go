package money

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
)

// FromFloat turns a double in major units into minor units. The double is written
// as its shortest decimal form (10.1 is "10.1", not 10.0999999999999996447) and parsed
// like a string, so a value that is not a whole number of minor units is an error,
// never a rounding. Sums of doubles often are not: 0.1 + 0.2 is 0.30000000000000004.
func FromFloat(v float64, exp int) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: %v", ErrSyntax, v)
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	minor, err := Parse(s, exp, Format{})
	if err != nil {
		return 0, fmt.Errorf("%w (a double; round it in the query if that is intended)", err)
	}
	return minor, nil
}

// FromScaled turns coef * 10^scale in major units into minor units. This is how
// MongoDB Decimal128 and PostgreSQL numeric values come out of their drivers.
// Digits below the currency's minor unit must be zeros.
func FromScaled(coef *big.Int, scale, exp int) (int64, error) {
	shift := scale + exp
	n := new(big.Int).Set(coef)
	if shift >= 0 {
		if n.Sign() != 0 && shift > 19 {
			return 0, fmt.Errorf("%w: %se%d does not fit in int64 minor units", ErrSyntax, coef, scale)
		}
		n.Mul(n, pow10(shift))
	} else {
		var rem big.Int
		n.QuoRem(n, pow10(-shift), &rem)
		if rem.Sign() != 0 {
			return 0, fmt.Errorf("%w: %se%d has more decimal places than the currency's %d", ErrSyntax, coef, scale, exp)
		}
	}
	if !n.IsInt64() {
		return 0, fmt.Errorf("%w: %se%d does not fit in int64 minor units", ErrSyntax, coef, scale)
	}
	return n.Int64(), nil
}

// FromMajor turns a whole number of major units into minor units.
func FromMajor(n int64, exp int) (int64, error) {
	return FromScaled(big.NewInt(n), 0, exp)
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

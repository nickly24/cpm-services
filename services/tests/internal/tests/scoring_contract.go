package tests

import "math/big"

// legacyPercentage reproduces int(round(earned / maximum * 100, 2)).
// Round the actual binary float to cents with ties to even; changing the
// multiplication order alters outcomes at legacy decimal boundaries.
func legacyPercentage(earned, maximum int) int {
	if maximum <= 0 {
		return 0
	}
	percentage := float64(earned) / float64(maximum) * 100
	cents := new(big.Rat).SetFloat64(percentage)
	cents.Mul(cents, big.NewRat(100, 1))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(cents.Num(), cents.Denom(), remainder)
	comparison := new(big.Int).Lsh(remainder, 1).Cmp(cents.Denom())
	if comparison > 0 || (comparison == 0 && quotient.Bit(0) == 1) {
		quotient.Add(quotient, big.NewInt(1))
	}
	return int(quotient.Int64() / 100)
}

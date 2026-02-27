package service

import "github.com/shopspring/decimal"

// decimalZero returns a fresh decimal.Zero value.
// Using a helper avoids repeating decimal.NewFromInt(0) callsites.
func decimalZero() decimal.Decimal {
	return decimal.NewFromInt(0)
}

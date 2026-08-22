package feedspec

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// ErrNoControlValue is returned by ControlTotalAccumulator.Add for a row whose
// control-total column did not produce a value (missing, or unparseable). Such a
// row is already rejected, so the caller normally records the reject and skips
// the contribution; the error exists so the omission is a decision, not an
// accident.
var ErrNoControlValue = errors.New("feedspec: row has no usable control-total value")

// ControlTotalAccumulator sums the feed's control-total column across DATA rows
// in exact decimal arithmetic (shopspring/decimal — never a float, CLAUDE.md
// §3), and compares the result against the trailer's declared total at the
// column's scale.
//
// It is the only arithmetic in this package that touches money, and it is exact
// by construction: the sum of n values of scale 2 is a value of scale 2.
type ControlTotalAccumulator struct {
	column string
	scale  int32
	sum    decimal.Decimal
	rows   int
}

// NewControlTotalAccumulator returns an accumulator for this feed's
// trailer.control_total_column.
func (f *Feed) NewControlTotalAccumulator() *ControlTotalAccumulator {
	return &ControlTotalAccumulator{column: f.ControlTotalColumn, scale: f.ControlTotalScale()}
}

// Column is the name of the summed column.
func (a *ControlTotalAccumulator) Column() string { return a.column }

// Add adds a row's control-total value. Rows whose control column has no usable
// value contribute nothing and return ErrNoControlValue.
func (a *ControlTotalAccumulator) Add(r RowResult) error {
	v, ok := r.Value(a.column)
	if !ok || !v.Present || v.Type != TypeDecimal {
		return fmt.Errorf("%w: column %s", ErrNoControlValue, a.column)
	}
	a.AddDecimal(v.Dec)
	return nil
}

// AddDecimal adds one exact decimal to the running total.
func (a *ControlTotalAccumulator) AddDecimal(d decimal.Decimal) {
	a.sum = a.sum.Add(d)
	a.rows++
}

// Result is the exact sum, at the control column's scale.
func (a *ControlTotalAccumulator) Result() decimal.Decimal {
	return a.sum.Round(a.scale)
}

// Rows is the number of values summed.
func (a *ControlTotalAccumulator) Rows() int { return a.rows }

// Matches reports whether the declared trailer total equals the computed total
// at the control column's scale — an exact decimal comparison, so 1801.00 and
// 1801 match (same value) while 1801.01 does not.
func (a *ControlTotalAccumulator) Matches(declared decimal.Decimal) bool {
	return a.Result().Equal(declared.Round(a.scale))
}

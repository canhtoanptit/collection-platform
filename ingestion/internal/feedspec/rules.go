package feedspec

import (
	"errors"
	"fmt"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/shopspring/decimal"
)

// maxCELScaledMagnitude is the largest |value × 10^scale| a decimal column may
// carry into a CEL rule: 2^52. Below it, the scaled value is an exact float64
// integer, so two values from the same column stay ordered and equal values stay
// equal — everything a comparative predicate needs. At scale 2 that is
// ±45,035,996,273,704.96, four orders of magnitude above any realistic account
// balance. Above it, feedspec reports ReasonDecimalExceedsRulePrecision instead
// of comparing at reduced precision: an unfaithful comparison is worse than a
// rejected row, because nobody would ever see it.
//
// Control totals do not use this path at all — they are exact decimals (see
// ControlTotalAccumulator).
//
// It is computed once at initialisation and never written: this is the package's
// only package-level value, and it is a constant in everything but the Go type
// system (decimal.Decimal cannot be a const).
var maxCELScaledMagnitude = decimal.NewFromInt(int64(1) << 52)

// compileRules builds the CEL environment from the column declarations and
// compiles every rule ONCE, at Load. A rule that does not compile, or whose
// result is not a bool, is a contract error — never a per-row surprise.
func compileRules(f *Feed) error {
	opts := make([]cel.EnvOption, 0, len(f.Columns)+1)
	// Rules are written by analysts against a bank feed, so `amount > 0` must
	// mean what it says even though amount is a double and 0 is an int
	// literal. cel-go's cross-type numeric comparison is exact (it does not
	// round either side to compare them), so this widens what compiles without
	// widening what is true.
	opts = append(opts, cel.CrossTypeNumericComparisons(true))
	for _, c := range f.Columns {
		opts = append(opts, cel.Variable(c.Name, celType(c)))
	}
	env, err := cel.NewEnv(opts...)
	if err != nil {
		return fmt.Errorf("building rule environment: %w", err)
	}

	var errs []error
	for i := range f.Rules {
		r := &f.Rules[i]
		ast, iss := env.Compile(r.Expr)
		if iss != nil && iss.Err() != nil {
			errs = append(errs, fmt.Errorf("business_rules[%d] (%s): expr does not compile: %w", i, r.ID, iss.Err()))
			continue
		}
		if !ast.OutputType().IsExactType(cel.BoolType) {
			errs = append(errs, fmt.Errorf("business_rules[%d] (%s): expr must evaluate to bool, got %s", i, r.ID, ast.OutputType()))
			continue
		}
		prg, err := env.Program(ast)
		if err != nil {
			errs = append(errs, fmt.Errorf("business_rules[%d] (%s): building program: %w", i, r.ID, err))
			continue
		}
		r.program = prg
	}
	return errors.Join(errs...)
}

// celType maps a column to its rule variable type (SPEC.md §6). Optional columns
// are dyn because they can be null, which forces a rule that touches one to
// null-check it instead of silently comparing against a zero value.
func celType(c Column) *cel.Type {
	if !c.Required {
		return cel.DynType
	}
	switch c.Type {
	case TypeInteger:
		return cel.IntType
	case TypeDecimal:
		return cel.DoubleType
	case TypeString, TypeEnum, TypeDate:
		return cel.StringType
	default:
		// Unreachable: Load rejects unknown types. Dyn is the safe default —
		// it compiles, and any mistyped comparison fails at eval, which is
		// reported as an ERROR rather than passing silently.
		return cel.DynType
	}
}

// activation builds the CEL variable bindings for one row and reports the cells
// that cannot be carried into a rule faithfully.
func (f *Feed) activation(values []Value) (map[string]any, []Failure) {
	vars := make(map[string]any, len(values))
	var fails []Failure
	for i, v := range values {
		col := f.Columns[i]
		if !v.Present {
			vars[col.Name] = types.NullValue
			continue
		}
		switch col.Type {
		case TypeInteger:
			vars[col.Name] = v.Int
		case TypeDecimal:
			if v.Dec.Shift(int32(col.Scale)).Abs().GreaterThan(maxCELScaledMagnitude) { //nolint:gosec // scale is validated 0..9 at load
				fails = append(fails, Failure{
					Column:   col.Name,
					Reason:   ReasonDecimalExceedsRulePrecision,
					Severity: SeverityError,
					Detail: fmt.Sprintf("value %s exceeds the exact-comparison range for business rules (|value x 10^%d| > 2^52)",
						v.Dec, col.Scale),
				})
				continue
			}
			vars[col.Name] = v.Dec.InexactFloat64()
		case TypeString, TypeEnum, TypeDate:
			vars[col.Name] = v.Raw
		default:
			vars[col.Name] = v.Raw
		}
	}
	return vars, fails
}

// evalRules runs every compiled rule against one row's activation. A rule
// returning false is a failure at the rule's declared severity; a rule that
// cannot be evaluated (null dereference, type error at run time) is always an
// ERROR — fail closed, because an unevaluated rule proves nothing about the row.
func (f *Feed) evalRules(vars map[string]any) []Failure {
	var fails []Failure
	for _, r := range f.Rules {
		if r.program == nil {
			fails = append(fails, Failure{
				RuleID:   r.ID,
				Reason:   ReasonRuleEvaluationError,
				Severity: SeverityError,
				Detail:   "rule was not compiled: feed was not produced by feedspec.Load",
			})
			continue
		}
		out, _, err := r.program.Eval(vars)
		if err != nil {
			fails = append(fails, Failure{
				RuleID:   r.ID,
				Reason:   ReasonRuleEvaluationError,
				Severity: SeverityError,
				Detail:   fmt.Sprintf("%s: %v", r.Expr, err),
			})
			continue
		}
		ok, isBool := boolValue(out)
		if !isBool {
			fails = append(fails, Failure{
				RuleID:   r.ID,
				Reason:   ReasonRuleEvaluationError,
				Severity: SeverityError,
				Detail:   fmt.Sprintf("%s: result is %s, not bool", r.Expr, out.Type()),
			})
			continue
		}
		if !ok {
			fails = append(fails, Failure{
				RuleID:   r.ID,
				Reason:   ReasonBusinessRuleFailed,
				Severity: r.Severity,
				Detail:   r.Expr,
			})
		}
	}
	return fails
}

func boolValue(v ref.Val) (bool, bool) {
	b, ok := v.Value().(bool)
	return b, ok
}

// Package feedspec is the pure validation library for the platform's batch file
// feeds. It turns a file-feed contract (contracts/files/<feed_id>.v<N>.yaml,
// meta-schema contracts/files/SPEC.md) into a validator for the D§21 line
// format:
//
//	HEADER,<FEED_CODE>,<YYYYMMDD>,<record_count>
//	DATA,<col1>,<col2>,...,<colN>
//	TRAILER,<record_count>,<control_total>
//
// # Purity
//
// The package performs no I/O of its own beyond reading contract bytes from an
// fs.FS the caller supplies, and validating from an io.Reader the caller opens.
// It has no globals, no logging, no clock, and no database. The pipeline worker
// (ING-4) owns the SFTP session, S3 objects, file registry rows and quarantine
// records; this package owns only the answer to "is this file valid, and if not,
// exactly which cells are wrong". That split is what makes the fault matrix
// testable from golden files instead of from a live cluster.
//
// # Two layers of API
//
// Stateless, on *Feed — usable on their own, in any order:
//
//	feed.ValidateHeader(line, businessDate)  // D§21 header record
//	feed.ValidateRow(record)                 // types, constraints, business rules
//	feed.ParseTrailer(line)                  // declared count + control total
//	feed.MatchFilename(name)                 // filename_regex -> business_date
//
// Stateful, per file, on *Validator — accumulates the counts and the control
// total the trailer is checked against:
//
//	v := feed.NewValidator(businessDate)
//	v.ValidateHeader(line)   // once, first
//	v.ValidateRow(record)    // n times
//	v.ValidateTrailer(line)  // once, after the rows: cross-checks counts+total
//	v.Result()               // FileResult: counts, totals, every Failure
//
// Feed.ValidateFile drives the whole sequence over an io.Reader and is what
// ING-4 and the golden tests use.
//
// # Validation order (D§21, A§32)
//
// header → field count → mandatory fields → data types → business rules →
// trailer → record count → control total. A later check is meaningless if an
// earlier one failed, so the order is normative, not incidental:
//
//   - business rules are evaluated only for rows in which every column produced
//     a usable typed value (no parse failure, no missing required field);
//   - a rule that cannot be evaluated is an ERROR whatever its declared
//     severity — an unevaluated rule proves nothing, so it fails closed;
//   - the computed control total sums only rows whose control-total column
//     parsed, and a file with any ERROR row is quarantined whole (ING-4 step 4),
//     because a partial load makes the declared total unreconcilable.
//
// # Money, and why business rules see float64
//
// Control totals are summed with shopspring/decimal and compared to the trailer
// as exact decimals at the column's scale. No monetary value is ever computed in
// a float (CLAUDE.md §3).
//
// Business rules are a different problem: they are comparative predicates
// (od_amt <= curr_bal, amount > 0), evaluated by cel-go, and CEL has no decimal
// type. The three options were a lossy double, a string (which makes <= lexical
// and therefore wrong for numbers), or a custom decimal extension type — a
// private rule dialect that no other tool could read or that a reviewer could
// verify by eye. This package chooses double, and then enforces the boundary
// where double stops being faithful rather than documenting a hope: a decimal
// whose value scaled by 10^scale exceeds 2^52 (±45 trillion at scale 2) is
// reported as ReasonDecimalExceedsRulePrecision (ERROR) instead of being
// compared at reduced precision. Within that bound, two values parsed from the
// same decimal text convert to the same float64 and values one unit-in-the-last-
// place apart stay ordered, which is all a comparison needs. See
// maxCELScaledMagnitude in rules.go.
package feedspec

// Package rules holds the CY2026 hospital price transparency checklist and
// the pure logic that scores an mrf.Source against it. Nothing in this
// package does I/O — it only reads what mrf.Source hands it — which is
// deliberate: these are the functions worth unit testing thoroughly (see
// checklist_test.go), the same way you'd keep business rules out of a
// Spring @Controller and in a plain, easily-mocked @Service.
package rules

import "fmt"

// CheckResult is the outcome of one hospital-level rule: something either
// true or false about the file as a whole (does it have an attestation? a
// Type 2 NPI?), as opposed to a rule scored across many item rows.
type CheckResult struct {
	RuleID      string
	Description string
	Passed      bool
	Detail      string
}

// ItemRuleSummary is the outcome of one item-level rule, tallied across
// every row the file contained. A compliance officer cares about "43% of
// your rows are missing a negotiated charge" more than a single pass/fail
// bit — CMS's own research found hospitals are rarely 0% or 100% compliant
// on any one field — so this reports a rate, not just a boolean.
type ItemRuleSummary struct {
	RuleID      string
	Description string
	RowsChecked int64
	RowsFailed  int64
	// SampleFailures holds up to sampleFailureCap human-readable pointers
	// to specific failing rows (line/item number + description), so a
	// compliance officer can go find and fix them — not just told a
	// percentage. Capped so a file that's 90% wrong doesn't blow up the
	// report itself.
	SampleFailures []string
}

// sampleFailureCap bounds how many example failures one rule records. The
// tally (RowsFailed) stays exact no matter how many there are; only the
// stored examples stop accumulating. Twenty is enough for a compliance
// officer to recognize the pattern behind a failing rule, while keeping a
// report on a file that is 90% non-compliant from becoming as unmanageable
// as the file itself.
const sampleFailureCap = 20

// Passed reports whether every row that was checked satisfied this rule.
// A rule with zero rows checked (an empty file) is reported as failed —
// "no data to check" is not compliance.
func (s ItemRuleSummary) Passed() bool {
	return s.RowsChecked > 0 && s.RowsFailed == 0
}

// ComplianceRate returns the fraction of checked rows that passed, from 0.0
// to 1.0. Returns 0 for a rule that checked zero rows.
func (s ItemRuleSummary) ComplianceRate() float64 {
	if s.RowsChecked == 0 {
		return 0
	}
	return float64(s.RowsChecked-s.RowsFailed) / float64(s.RowsChecked)
}

// recordFailure counts one failing row and keeps its pointer as an example
// while there is room under sampleFailureCap. Pointer receiver, unlike
// Passed and ComplianceRate above, because this is the one method here that
// mutates — Evaluate calls it through the summaries slice it is
// accumulating into.
func (s *ItemRuleSummary) recordFailure(pointer string) {
	s.RowsFailed++
	if len(s.SampleFailures) < sampleFailureCap {
		s.SampleFailures = append(s.SampleFailures, pointer)
	}
}

// Report is the full result of checking one MRF against the CY2026
// checklist: what was true about the file as a whole, and how the file's
// item rows scored, rule by rule.
type Report struct {
	Format         string
	HospitalName   string
	HospitalChecks []CheckResult
	ItemChecks     []ItemRuleSummary
	RowsProcessed  int64

	// ParseErr is set when reading the file stopped early because it
	// stopped being valid JSON or CSV partway through (as opposed to a row
	// simply being incomplete, which is a compliance finding scored above,
	// not a parse failure). RowsProcessed still reflects whatever was
	// successfully read before that point — a partial report on a 2 GB
	// file that broke at row 400,000 is far more useful to a compliance
	// officer than no report at all.
	ParseErr string
}

// OverallPassed reports whether every hospital-level check passed and
// every item-level rule was satisfied by 100% of rows, with no parse
// error. This is deliberately strict — see ARCHITECTURE.md on why a
// pass/fail summary line exists alongside the per-rule compliance rates,
// not instead of them.
func (r Report) OverallPassed() bool {
	if r.ParseErr != "" {
		return false
	}
	for _, c := range r.HospitalChecks {
		if !c.Passed {
			return false
		}
	}
	for _, c := range r.ItemChecks {
		if !c.Passed() {
			return false
		}
	}
	return true
}

// checkNonEmpty builds the CheckResult for the several hospital-level rules
// that amount to "this field has to be filled in," echoing the value back
// in Detail on success so the report shows what was found rather than only
// that something was. Rules needing more than presence — licensure, NPI
// shape, attestation confirmation — have their own functions in
// checklist.go.
func checkNonEmpty(id, description, value string) CheckResult {
	if value == "" {
		return CheckResult{RuleID: id, Description: description, Passed: false, Detail: "not present in the file"}
	}
	return CheckResult{RuleID: id, Description: description, Passed: true, Detail: fmt.Sprintf("found: %q", value)}
}

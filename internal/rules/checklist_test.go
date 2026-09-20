package rules

import (
	"testing"

	"github.com/bobbylon127/mrfsentinel/internal/mrf"
)

// fakeSource is a hand-rolled stand-in for mrf.Source, backed by a plain
// slice — the same role a Mockito mock of a repository interface plays in
// a Spring test: it lets Evaluate be tested against known inputs without
// any real file or network I/O.
type fakeSource struct {
	format mrf.Format
	meta   mrf.Metadata
	rows   []mrf.Row
	i      int
}

// Format returns whichever format the test declared; Evaluate only copies
// it into the report, so nothing here depends on it being truthful.
func (f *fakeSource) Format() mrf.Format { return f.format }

// Metadata returns the hand-built hospital metadata under test, which is
// what drives every hospital-level check.
func (f *fakeSource) Metadata() mrf.Metadata { return f.meta }

// Next walks the canned row slice once and then reports mrf.ErrDone,
// mimicking a real Source's single-pass contract — nothing rewinds, so a
// fakeSource is good for exactly one Evaluate call.
func (f *fakeSource) Next() (mrf.Row, error) {
	if f.i >= len(f.rows) {
		return mrf.Row{}, mrf.ErrDone
	}
	row := f.rows[f.i]
	f.i++
	return row, nil
}

// ptr takes the address of a float64 literal, which Go does not allow
// inline. Charge fields are pointers throughout mrf.Row so that absent
// stays distinguishable from zero, and this keeps the fixtures below
// readable in spite of that.
func ptr(f float64) *float64 { return &f }

// iptr is ptr for the one int-valued field the checklist reads, CY2026's
// allowed-amount count.
func iptr(n int) *int { return &n }

// fullyCompliantRow satisfies every item-level rule in the checklist.
func fullyCompliantRow(n int64) mrf.Row {
	return mrf.Row{
		LineNumber:         n,
		Description:        "Basic metabolic panel",
		Codes:              []mrf.Code{{Value: "80048", Type: "CPT"}},
		GrossCharge:        ptr(200),
		DiscountedCash:     ptr(150),
		PayerName:          "Acme Health",
		NegotiatedDollar:   ptr(175.5),
		DeidentifiedMin:    ptr(50),
		DeidentifiedMax:    ptr(300),
		MedianAmount:       ptr(170),
		Pctl10Amount:       ptr(120),
		Pctl90Amount:       ptr(250),
		AllowedAmountCount: iptr(42),
	}
}

// TestEvaluate_FullyCompliantFile is the positive case: metadata satisfying
// every hospital-level rule, and rows satisfying every item-level rule,
// must produce OverallPassed. It guards against the failure mode where a
// rule is accidentally impossible to satisfy — which a suite made only of
// negative cases would happily report as working.
func TestEvaluate_FullyCompliantFile(t *testing.T) {
	src := &fakeSource{
		format: mrf.FormatJSON,
		meta: mrf.Metadata{
			HospitalName:          "Test General Hospital",
			LastUpdatedOn:         "2026-06-01",
			LicenseNumber:         "12345",
			LicenseState:          "CA",
			Type2NPIs:             []string{"1234567890"},
			AttesterName:          "Jane Doe, CEO",
			AttestationFieldFound: true,
			AttestationConfirmed:  true,
		},
		rows: []mrf.Row{fullyCompliantRow(1), fullyCompliantRow(2)},
	}

	report := Evaluate(src)

	if !report.OverallPassed() {
		t.Fatalf("OverallPassed() = false, want true; hospital=%+v items=%+v", report.HospitalChecks, report.ItemChecks)
	}
	if report.RowsProcessed != 2 {
		t.Errorf("RowsProcessed = %d, want 2", report.RowsProcessed)
	}
	for _, c := range report.HospitalChecks {
		if !c.Passed {
			t.Errorf("hospital check %s failed unexpectedly: %s", c.RuleID, c.Detail)
		}
	}
}

// TestEvaluate_FlagsMissingAttestationAndPercentiles is the case this
// product exists for: a file that was perfectly compliant before CY2026 and
// is not any more. It asserts the specific rules that must fail (NPI,
// attestation, percentiles) rather than only that something failed, and
// checks the tally and the sampled failure alongside — the numbers a
// compliance officer actually acts on.
func TestEvaluate_FlagsMissingAttestationAndPercentiles(t *testing.T) {
	src := &fakeSource{
		format: mrf.FormatCSVTall,
		meta: mrf.Metadata{
			HospitalName: "Legacy Format Hospital",
			// No attestation, no NPI, no license — a hospital that hasn't
			// updated its file for CY2026 at all.
		},
		rows: []mrf.Row{
			{
				LineNumber:       1,
				Description:      "Comprehensive panel",
				Codes:            []mrf.Code{{Value: "80053", Type: "CPT"}},
				GrossCharge:      ptr(300),
				DiscountedCash:   ptr(220),
				NegotiatedDollar: ptr(260),
				DeidentifiedMin:  ptr(100),
				DeidentifiedMax:  ptr(400),
				// Old pre-2026 field instead of the new percentile fields:
				EstimatedAmount: ptr(250),
			},
		},
	}

	report := Evaluate(src)

	if report.OverallPassed() {
		t.Fatal("OverallPassed() = true, want false")
	}

	var npiCheck, attestationCheck CheckResult
	for _, c := range report.HospitalChecks {
		switch c.RuleID {
		case "CY26-TYPE2-NPI":
			npiCheck = c
		case "CY26-ATTESTATION":
			attestationCheck = c
		}
	}
	if npiCheck.Passed {
		t.Error("CY26-TYPE2-NPI passed, want failed (no NPI in this file)")
	}
	if attestationCheck.Passed {
		t.Error("CY26-ATTESTATION passed, want failed (no attestation in this file)")
	}

	var percentileSummary ItemRuleSummary
	for _, s := range report.ItemChecks {
		if s.RuleID == "CY26-PERCENTILES" {
			percentileSummary = s
		}
	}
	if percentileSummary.Passed() {
		t.Error("CY26-PERCENTILES passed, want failed (row only has the legacy estimated_amount field)")
	}
	if percentileSummary.RowsChecked != 1 || percentileSummary.RowsFailed != 1 {
		t.Errorf("CY26-PERCENTILES tally = checked=%d failed=%d, want 1/1", percentileSummary.RowsChecked, percentileSummary.RowsFailed)
	}
	if len(percentileSummary.SampleFailures) != 1 {
		t.Errorf("SampleFailures = %v, want exactly 1 entry", percentileSummary.SampleFailures)
	}
}

// TestEvaluate_PartialResultsOnParseError checks that a file which breaks
// partway through still yields everything read up to that point, with the
// error recorded rather than thrown away. On a multi-gigabyte file that
// went bad at row 400,000, a partial report is far more useful than none —
// see rules.Report.ParseErr.
func TestEvaluate_PartialResultsOnParseError(t *testing.T) {
	src := &partialFailSource{
		meta:      mrf.Metadata{HospitalName: "Broken File Hospital"},
		goodRows:  3,
		failAfter: 3,
	}

	report := Evaluate(src)

	if report.ParseErr == "" {
		t.Fatal("ParseErr = \"\", want a parse error recorded")
	}
	if report.RowsProcessed != 3 {
		t.Errorf("RowsProcessed = %d, want 3 (the rows read before the failure)", report.RowsProcessed)
	}
	if report.OverallPassed() {
		t.Error("OverallPassed() = true, want false when ParseErr is set")
	}
}

// partialFailSource simulates a file that parses correctly for a while and
// then hits a corrupt row — the case Report.ParseErr exists for.
type partialFailSource struct {
	meta      mrf.Metadata
	goodRows  int
	failAfter int
	i         int
}

// Format is fixed here; this fake exists to exercise the failure path, not
// any format-specific behavior.
func (f *partialFailSource) Format() mrf.Format { return mrf.FormatCSVTall }

// Metadata returns the test's hospital metadata, unaffected by the row
// failure this source simulates.
func (f *partialFailSource) Metadata() mrf.Metadata { return f.meta }

// Next hands back compliant rows until failAfter is reached and then
// returns a *mrf.ParseError — deliberately not mrf.ErrDone, since the whole
// point is to distinguish "the file ended" from "the file stopped making
// sense".
func (f *partialFailSource) Next() (mrf.Row, error) {
	if f.i >= f.failAfter {
		return mrf.Row{}, &mrf.ParseError{Format: mrf.FormatCSVTall, Line: int64(f.i + 4), Err: errCorruptRow}
	}
	f.i++
	return fullyCompliantRow(int64(f.i)), nil
}

// errCorruptRow is the stand-in cause wrapped inside the simulated parse
// failure above.
var errCorruptRow = errStr("simulated corrupt row")

// errStr is a minimal error implementation, used instead of errors.New so
// the sentinel above can be declared as a plain constant-like value without
// pulling errors into this test file for one line.
type errStr string

// Error satisfies the error interface with the string's own contents.
func (e errStr) Error() string { return string(e) }

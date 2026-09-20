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

func (f *fakeSource) Format() mrf.Format     { return f.format }
func (f *fakeSource) Metadata() mrf.Metadata { return f.meta }
func (f *fakeSource) Next() (mrf.Row, error) {
	if f.i >= len(f.rows) {
		return mrf.Row{}, mrf.ErrDone
	}
	row := f.rows[f.i]
	f.i++
	return row, nil
}

func ptr(f float64) *float64 { return &f }
func iptr(n int) *int        { return &n }

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

func (f *partialFailSource) Format() mrf.Format     { return mrf.FormatCSVTall }
func (f *partialFailSource) Metadata() mrf.Metadata { return f.meta }
func (f *partialFailSource) Next() (mrf.Row, error) {
	if f.i >= f.failAfter {
		return mrf.Row{}, &mrf.ParseError{Format: mrf.FormatCSVTall, Line: int64(f.i + 4), Err: errCorruptRow}
	}
	f.i++
	return fullyCompliantRow(int64(f.i)), nil
}

var errCorruptRow = errStr("simulated corrupt row")

type errStr string

func (e errStr) Error() string { return string(e) }

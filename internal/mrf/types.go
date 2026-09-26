// Package mrf reads a hospital's published "machine-readable file" (MRF) —
// the standard-charges dataset every US hospital must publish under the
// Hospital Price Transparency rule (45 CFR 180) — without loading the whole
// file into memory first.
//
// Why that matters: some hospital systems' MRFs run to multiple gigabytes
// (a large system publishes one row per procedure per payer per plan, which
// multiplies out fast). Loading a file like that into a []Row slice before
// looking at any of it is the equivalent of a Java program calling
// Files.readAllBytes() on a multi-gigabyte file, or Angular's HttpClient
// buffering an entire response body before your subscribe() callback ever
// runs — it works until it doesn't, and "doesn't" here means an
// out-of-memory crash on exactly the largest, most important hospital
// systems to check.
//
// Instead, everything in this package is built around a Source: a value you
// read from one row at a time, the same shape as a Java Iterator<Row> or a
// database ResultSet you step through with next(). The caller (see
// internal/rules) folds each row into a running tally as it goes, so peak
// memory use stays roughly constant regardless of file size.
package mrf

import "fmt"

// Format identifies which of CMS's published MRF layouts a Source is
// reading. CMS allows three: one JSON layout, and two CSV layouts ("tall",
// one row per payer-plan-charge combination, and "wide", one row per item
// with a separate pair of columns for every payer-plan combination in the
// file). See the CSV/JSON data dictionaries linked from README.md's Sources
// section — the field names in this package are taken from those, not
// guessed.
type Format string

// The three formats CMS actually permits, plus FormatUnknown for a file
// whose shape could not be determined at all (see sniff.go's Open, which is
// what decides). These are the values stored in a validation run's `format`
// column and shown on the report page, so they are stable strings rather
// than an iota-based enum whose numbers would shift if one were inserted.
const (
	FormatJSON    Format = "json"
	FormatCSVTall Format = "csv_tall"
	FormatCSVWide Format = "csv_wide"
	FormatUnknown Format = "unknown"
)

// Metadata is the hospital-level information that appears once per MRF,
// outside the per-item rows — the header, roughly, if you think of the file
// as a spreadsheet with a title block above the data.
type Metadata struct {
	HospitalName string
	// LastUpdatedOn is kept as the raw string the file published (CMS
	// specifies ISO 8601, e.g. "2026-06-01"), not a parsed time.Time — a
	// hospital publishing this field in the wrong format is itself a
	// compliance finding worth surfacing, not something to silently coerce.
	LastUpdatedOn string
	Version       string
	LicenseNumber string
	LicenseState  string

	// Type2NPIs are the hospital's own organizational NPIs — required by
	// the CY2026 rule so a validator (or CMS) can tie a file back to a
	// specific hospital unambiguously. Type 2 = organizational, as opposed
	// to a Type 1 NPI, which identifies an individual clinician.
	Type2NPIs []string

	// AttesterName and the two attestation fields below are new for
	// CY2026: a named senior officer must personally attest the data is
	// "true, accurate, and complete." See ARCHITECTURE.md for why this is
	// the single field this whole product exists to help a hospital get
	// right before that person signs.
	AttesterName          string
	AttestationStatement  string
	AttestationConfirmed  bool
	AttestationFieldFound bool // true if an attestation block existed at all, even if empty/unconfirmed
}

// Code is one billing/accounting code attached to a charge row (e.g. a CPT,
// HCPCS, DRG, or NDC code) together with the code system it belongs to.
type Code struct {
	Value string
	Type  string
}

// Row is one line of standard-charge data: what CMS's data dictionary calls
// an "item or service" combined with one payer-plan's charge for it (in the
// tall CSV format and the JSON format, this pairing is already one row; the
// wide CSV format instead spreads every payer-plan pair across columns of a
// single item row — see csv.go for how that gets reshaped into this same
// per-payer Row shape so the rest of this package never has to know which
// layout it came from).
type Row struct {
	// LineNumber is 1-indexed and counts data rows only (header rows are
	// not counted), for pointing a compliance officer at the exact spot in
	// their own file a finding came from.
	LineNumber int64

	Description string
	Codes       []Code
	Setting     string

	GrossCharge    *float64
	DiscountedCash *float64

	PayerName string
	PlanName  string

	NegotiatedDollar     *float64
	NegotiatedPercentage *float64
	NegotiatedAlgorithm  string

	DeidentifiedMin *float64
	DeidentifiedMax *float64

	// MedianAmount, Pctl10Amount, Pctl90Amount and AllowedAmountCount are
	// the CY2026 replacement for the old single EstimatedAmount field
	// (still recognized below so this parser can tell the two formats
	// apart and flag a file still using the pre-2026 shape).
	MedianAmount       *float64
	Pctl10Amount       *float64
	Pctl90Amount       *float64
	AllowedAmountCount *int

	EstimatedAmount *float64
}

// HasAnyNegotiatedCharge reports whether this row records a payer-specific
// negotiated charge in any of the three forms CMS accepts (a flat dollar
// amount, a percentage, or a described algorithm) — CMS requires at least
// one, not all three.
func (r Row) HasAnyNegotiatedCharge() bool {
	return r.NegotiatedDollar != nil || r.NegotiatedPercentage != nil || r.NegotiatedAlgorithm != ""
}

// HasCY2026PercentileFields reports whether this row reports the CY2026
// dollar-percentile fields (median, 10th and 90th percentile, and a count)
// that replace the old single "estimated allowed amount" field.
func (r Row) HasCY2026PercentileFields() bool {
	return r.MedianAmount != nil && r.Pctl10Amount != nil && r.Pctl90Amount != nil && r.AllowedAmountCount != nil
}

// Source streams one MRF's contents: its Metadata once, then its Row values
// one at a time via Next, which returns io.EOF (wrapped by errors.Is-
// compatible sentinel ErrDone) once the file is exhausted. Implementations:
// jsonSource (json.go) and csvSource (csv.go).
type Source interface {
	Format() Format
	Metadata() Metadata
	Next() (Row, error)
}

// ParseError wraps a failure to make sense of the file's structure itself
// (as opposed to a single row being incomplete, which is a compliance
// finding, not a parse failure) — a file that isn't valid JSON, or a CSV
// missing its header row entirely, is a ParseError.
type ParseError struct {
	Format Format
	Line   int64
	Err    error
}

// Error renders the failure with the line number when one is known. A
// hospital's compliance officer reading this on the report page is being
// pointed at a specific spot in a file that may be millions of lines long,
// so "at line 412,907" is the difference between an actionable message and
// a useless one; Line is 0 when the failure was structural (see openCSV,
// which cannot attribute a missing header row to a data line).
func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("mrf: %s parse error at line %d: %v", e.Format, e.Line, e.Err)
	}
	return fmt.Sprintf("mrf: %s parse error: %v", e.Format, e.Err)
}

// Unwrap exposes the underlying cause to errors.Is and errors.As, so a
// caller can still match on, say, an io.ErrUnexpectedEOF buried inside this
// wrapper. This is Go's equivalent of a Java exception's getCause(), except
// that the standard library's matching helpers walk the chain for you.
func (e *ParseError) Unwrap() error { return e.Err }

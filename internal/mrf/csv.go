package mrf

import (
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// CSV layout, per CMS's own CSV data dictionary (documentation/CSV/README.md
// in CMSgov/hospital-price-transparency — see README.md's Sources section):
//
//	line 1: header row for hospital-level metadata columns
//	line 2: one data row holding those metadata values
//	line 3: header row for the per-item columns
//	line 4+: one data row per item (tall) or per item-with-all-its-payers (wide)
//
// "Tall" repeats a row per payer-plan combination (payer_name/plan_name are
// their own columns); "wide" instead has one row per item, with every
// payer-plan's charge in its own pair of columns
// (`standard_charge | Aetna | PPO Gold | negotiated_dollar`, and so on,
// repeated per payer-plan found in the file). openCSV below tells the two
// apart by checking whether the item header row contains a literal
// "payer_name" column.

// wideColumnPattern matches a wide-format column header for one
// payer-plan's negotiated charge or percentile figure, e.g.
// "standard_charge | Aetna | PPO Gold | negotiated_dollar" or
// "median_amount | Aetna | PPO Gold". Group 3 is the field the column holds.
var wideColumnPattern = regexp.MustCompile(
	`^(?:standard_charge \| .+ \| .+ \| (negotiated_dollar|negotiated_percentage|negotiated_algorithm|methodology)|(median_amount|10th_percentile|90th_percentile|count) \| .+ \| .+)$`,
)

// codeColumnPattern matches "code | 1", "code | 2", ... (there can be more
// than one billing code per item, e.g. a CPT code and an NDC code together).
var codeColumnPattern = regexp.MustCompile(`^code \| (\d+)$`)

type csvSource struct {
	format   Format
	meta     Metadata
	reader   *csv.Reader
	itemCols map[string]int // header name -> column index, for the item rows
	// wideNegotiatedCols / widePercentileCols hold the column indexes of
	// every payer-plan's negotiated-charge / percentile columns, for the
	// wide format's reduced-fidelity aggregate check (see package doc and
	// ARCHITECTURE.md's "Honest scoping" section for why this is
	// aggregated rather than expanded to one Row per payer-plan).
	wideNegotiatedCols []int
	widePercentileCols []int
	lineNum            int64
}

// openCSV reads the two header rows and the metadata row, then returns a
// Source positioned to stream item rows one at a time from r. r is not
// buffered further here — encoding/csv.Reader already reads incrementally
// from whatever io.Reader it's given, which for a hospital MRF fetched over
// HTTP is the response body stream itself (see fetch.go), so a multi-
// gigabyte file is never held in memory as a whole.
func openCSV(r io.Reader) (*csvSource, error) {
	cr := csv.NewReader(r)
	// Hospital MRFs are not perfectly uniform in column count row to row in
	// the wild (CMS's own FAQ acknowledges this); FieldsPerRecord = -1
	// disables encoding/csv's built-in column-count check so one short row
	// doesn't abort the whole file — the rule evaluator treats a missing
	// cell as "field absent" instead, which is the finding we actually want
	// to report.
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true

	metaHeader, err := cr.Read()
	if err != nil {
		return nil, &ParseError{Format: FormatUnknown, Line: 1, Err: fmt.Errorf("reading hospital metadata header row: %w", err)}
	}
	metaValues, err := cr.Read()
	if err != nil {
		return nil, &ParseError{Format: FormatUnknown, Line: 2, Err: fmt.Errorf("reading hospital metadata values row: %w", err)}
	}
	itemHeader, err := cr.Read()
	if err != nil {
		return nil, &ParseError{Format: FormatUnknown, Line: 3, Err: fmt.Errorf("reading item column header row: %w", err)}
	}

	meta := parseCSVMetadata(metaHeader, metaValues)

	itemCols := make(map[string]int, len(itemHeader))
	for i, name := range itemHeader {
		itemCols[strings.TrimSpace(name)] = i
	}

	format := FormatCSVTall
	var wideNeg, widePct []int
	if _, hasPayerName := itemCols["payer_name"]; !hasPayerName {
		format = FormatCSVWide
		for i, name := range itemHeader {
			name = strings.TrimSpace(name)
			m := wideColumnPattern.FindStringSubmatch(name)
			if m == nil {
				continue
			}
			if m[1] != "" { // standard_charge | ... | <field>
				wideNeg = append(wideNeg, i)
			} else { // median_amount|10th_percentile|90th_percentile|count | ...
				widePct = append(widePct, i)
			}
		}
	}

	return &csvSource{
		format:             format,
		meta:               meta,
		reader:             cr,
		itemCols:           itemCols,
		wideNegotiatedCols: wideNeg,
		widePercentileCols: widePct,
		lineNum:            3, // header rows consumed so far; item rows start at "line 4" in CMS's own numbering
	}, nil
}

func (s *csvSource) Format() Format     { return s.format }
func (s *csvSource) Metadata() Metadata { return s.meta }

// ErrDone is returned by Source.Next once every row has been read — analogous
// to io.EOF, which is exactly what it wraps; it exists as a named sentinel
// so callers in internal/validation can check for it with errors.Is without
// reaching into this package's choice of underlying reader.
var ErrDone = io.EOF

func (s *csvSource) Next() (Row, error) {
	record, err := s.reader.Read()
	if err != nil {
		if err == io.EOF {
			return Row{}, ErrDone
		}
		return Row{}, &ParseError{Format: s.format, Line: s.lineNum + 1, Err: err}
	}
	s.lineNum++

	cell := func(name string) string {
		i, ok := s.itemCols[name]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}

	row := Row{
		LineNumber:      s.lineNum - 3, // 1-indexed, counting only item data rows
		Description:     cell("description"),
		Setting:         cell("setting"),
		GrossCharge:     parseFloatCell(cell("standard_charge | gross")),
		DiscountedCash:  parseFloatCell(cell("standard_charge | discounted_cash")),
		DeidentifiedMin: parseFloatCell(cell("standard_charge | min")),
		DeidentifiedMax: parseFloatCell(cell("standard_charge | max")),
	}

	for header, idx := range s.itemCols {
		m := codeColumnPattern.FindStringSubmatch(header)
		if m == nil || idx >= len(record) {
			continue
		}
		value := strings.TrimSpace(record[idx])
		if value == "" {
			continue
		}
		typeIdx, ok := s.itemCols[header+" | type"]
		codeType := ""
		if ok && typeIdx < len(record) {
			codeType = strings.TrimSpace(record[typeIdx])
		}
		row.Codes = append(row.Codes, Code{Value: value, Type: codeType})
	}

	switch s.format {
	case FormatCSVTall:
		row.PayerName = cell("payer_name")
		row.PlanName = cell("plan_name")
		row.NegotiatedDollar = parseFloatCell(cell("standard_charge | negotiated_dollar"))
		row.NegotiatedPercentage = parseFloatCell(cell("standard_charge | negotiated_percentage"))
		row.NegotiatedAlgorithm = cell("standard_charge | negotiated_algorithm")
		row.MedianAmount = parseFloatCell(cell("median_amount"))
		row.Pctl10Amount = parseFloatCell(cell("10th_percentile"))
		row.Pctl90Amount = parseFloatCell(cell("90th_percentile"))
		row.AllowedAmountCount = parseIntCell(cell("count"))
		row.EstimatedAmount = parseFloatCell(cell("estimated_amount"))

	case FormatCSVWide:
		// Reduced-fidelity aggregate check, deliberately: a wide-format row
		// holds one column pair per payer-plan, so "does this item have
		// negotiated-charge data" is answered across ALL of them rather
		// than expanding this one CSV line into N Row values (one per
		// payer). See ARCHITECTURE.md.
		if anyNonEmpty(record, s.wideNegotiatedCols) {
			one := 1.0 // sentinel non-nil value; the checklist only asks "present or not" for wide rows
			row.NegotiatedDollar = &one
		}
		if anyNonEmpty(record, s.widePercentileCols) {
			one := 1.0
			row.MedianAmount = &one
			row.Pctl10Amount = &one
			row.Pctl90Amount = &one
			n := 1
			row.AllowedAmountCount = &n
		}
	}

	return row, nil
}

func anyNonEmpty(record []string, cols []int) bool {
	for _, i := range cols {
		if i < len(record) && strings.TrimSpace(record[i]) != "" {
			return true
		}
	}
	return false
}

func parseCSVMetadata(header, values []string) Metadata {
	get := func(name string) string {
		for i, h := range header {
			if strings.TrimSpace(h) == name && i < len(values) {
				return strings.TrimSpace(values[i])
			}
		}
		return ""
	}

	meta := Metadata{
		HospitalName:  get("hospital_name"),
		LastUpdatedOn: get("last_updated_on"),
		Version:       get("version"),
		AttesterName:  get("attester_name"),
	}

	for i, h := range header {
		h = strings.TrimSpace(h)
		if i >= len(values) {
			continue
		}
		v := strings.TrimSpace(values[i])
		switch {
		case strings.HasPrefix(h, "license_number"):
			if v != "" {
				meta.LicenseNumber = v
				// Header is "license_number | <state>" per the data
				// dictionary; pull the state out if present.
				if parts := strings.SplitN(h, "|", 2); len(parts) == 2 {
					meta.LicenseState = strings.TrimSpace(parts[1])
				}
			}
		case strings.HasPrefix(h, "type_2_npi"):
			if v != "" {
				meta.Type2NPIs = append(meta.Type2NPIs, v)
			}
		case strings.Contains(strings.ToLower(h), "attestation"):
			if v != "" {
				meta.AttestationFieldFound = true
				meta.AttestationStatement = v
				lower := strings.ToLower(v)
				meta.AttestationConfirmed = lower == "true" || lower == "yes" || strings.Contains(lower, "true, accurate, and complete")
			}
		}
	}
	if meta.AttesterName != "" {
		meta.AttestationFieldFound = true
	}

	return meta
}

func parseFloatCell(s string) *float64 {
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimPrefix(s, "$"), 64)
	if err != nil {
		return nil
	}
	return &f
}

func parseIntCell(s string) *int {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

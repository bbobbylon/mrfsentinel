package mrf

import (
	"encoding/json"
	"fmt"
	"io"
)

// JSON structure below matches CMS's Hospital Price Transparency JSON Data
// Dictionary v3.0 (documentation/JSON/README.md in
// CMSgov/hospital-price-transparency — see README.md's Sources section),
// confirmed field by field for every field this package reads. The one
// exception is noted on jsonPayerInformation.EstimatedAmount below.

type jsonLicenseInformation struct {
	LicenseNumber string `json:"license_number"`
	State         string `json:"state"`
}

type jsonAttestation struct {
	Statement    string `json:"attestation"`
	Confirmed    bool   `json:"confirm_attestation"`
	AttesterName string `json:"attester_name"`
}

type jsonCode struct {
	Code string `json:"code"`
	Type string `json:"type"`
}

type jsonPayerInformation struct {
	PayerName             string   `json:"payer_name"`
	PlanName              string   `json:"plan_name"`
	StandardChargeDollar  *float64 `json:"standard_charge_dollar"`
	StandardChargePercent *float64 `json:"standard_charge_percentage"`
	StandardChargeAlgo    string   `json:"standard_charge_algorithm"`
	MedianAmount          *float64 `json:"median_amount"`
	Percentile10          *float64 `json:"10th_percentile"`
	Percentile90          *float64 `json:"90th_percentile"`
	Count                 *int     `json:"count"`
	// EstimatedAmount is the pre-CY2026 field the four fields above
	// replace. Its presence at this same per-payer level — rather than up
	// on jsonStandardCharge — is inferred by analogy with the CSV tall
	// format (where estimated_amount is a column alongside
	// negotiated_dollar at row level), not independently re-confirmed
	// against the JSON dictionary's own text. See ARCHITECTURE.md's
	// "Honest scoping" section. If that inference is wrong, this field
	// simply never populates and rule CY26-PERCENTILES falls back to
	// judging files solely on whether the new percentile fields are
	// present — which is the check that actually matters for CY2026
	// compliance either way.
	EstimatedAmount *float64 `json:"estimated_amount"`
}

type jsonStandardCharge struct {
	GrossCharge       *float64               `json:"gross_charge"`
	DiscountedCash    *float64               `json:"discounted_cash"`
	Setting           string                 `json:"setting"`
	Minimum           *float64               `json:"minimum"`
	Maximum           *float64               `json:"maximum"`
	PayersInformation []jsonPayerInformation `json:"payers_information"`
}

type jsonItem struct {
	Description     string               `json:"description"`
	CodeInformation []jsonCode           `json:"code_information"`
	StandardCharges []jsonStandardCharge `json:"standard_charges"`
}

// jsonSource streams the "standard_charge_information" array one element at
// a time using encoding/json's token API, rather than json.Unmarshal-ing the
// whole file into a struct first. If you know XML parsing from the Java
// world: this is the same trade-off as SAX versus DOM — a DOM/Unmarshal
// parser is simpler to use but has to hold the entire document in memory at
// once; a SAX/token-stream parser like this one sees the document as a
// sequence of events (object-start, key, array-start, ...) and lets you
// decide, per event, whether to fully decode that piece or skip past it.
// Here, every top-level key EXCEPT standard_charge_information is small
// (a hospital's name, license info, ...) and gets fully decoded normally;
// standard_charge_information is where a real file's multiple gigabytes
// live, so that's the one key this parser treats as a stream of items
// instead of one big value.
type jsonSource struct {
	dec       *json.Decoder
	meta      Metadata
	itemIndex int64
	pending   []Row // rows flattened from the item currently being drained
	exhausted bool
}

// openJSON reads the top-level object's keys until it finds
// standard_charge_information, capturing every other key into Metadata
// along the way, then leaves the decoder positioned inside that array so
// Next can stream it. A file whose standard_charge_information key comes
// before some other metadata key it emits would have that later metadata
// silently missed — a known, named limitation (see the comment where the
// array is found, below), not a silent one.
func openJSON(r io.Reader) (*jsonSource, error) {
	dec := json.NewDecoder(r)

	tok, err := dec.Token()
	if err != nil {
		return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("reading opening token: %w", err)}
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("expected a JSON object at the top level, got %v", tok)}
	}

	src := &jsonSource{}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("reading a top-level key: %w", err)}
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("expected a string key, got %v", keyTok)}
		}

		if key == "standard_charge_information" {
			arrTok, err := dec.Token()
			if err != nil {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("opening standard_charge_information array: %w", err)}
			}
			if d, ok := arrTok.(json.Delim); !ok || d != '[' {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("standard_charge_information must be a JSON array, got %v", arrTok)}
			}
			src.dec = dec
			return src, nil
		}

		switch key {
		case "hospital_name":
			if err := dec.Decode(&src.meta.HospitalName); err != nil {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("decoding hospital_name: %w", err)}
			}
		case "last_updated_on":
			if err := dec.Decode(&src.meta.LastUpdatedOn); err != nil {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("decoding last_updated_on: %w", err)}
			}
		case "version":
			if err := dec.Decode(&src.meta.Version); err != nil {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("decoding version: %w", err)}
			}
		case "license_information":
			var v jsonLicenseInformation
			if err := dec.Decode(&v); err != nil {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("decoding license_information: %w", err)}
			}
			src.meta.LicenseNumber = v.LicenseNumber
			src.meta.LicenseState = v.State
		case "type_2_npi":
			if err := dec.Decode(&src.meta.Type2NPIs); err != nil {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("decoding type_2_npi: %w", err)}
			}
		case "attestation":
			var v jsonAttestation
			if err := dec.Decode(&v); err != nil {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("decoding attestation: %w", err)}
			}
			src.meta.AttestationStatement = v.Statement
			src.meta.AttestationConfirmed = v.Confirmed
			src.meta.AttesterName = v.AttesterName
			src.meta.AttestationFieldFound = true
		default:
			// Unknown or uninteresting key: decode into a throwaway value
			// purely to advance the decoder's cursor past it, the same way
			// a SAX handler ignores an element it doesn't care about but
			// still has to let the parser walk past.
			var discard json.RawMessage
			if err := dec.Decode(&discard); err != nil {
				return nil, &ParseError{Format: FormatJSON, Err: fmt.Errorf("skipping key %q: %w", key, err)}
			}
		}
	}

	// Reached the end of the object without ever finding
	// standard_charge_information — that's a valid (if useless) parse, not
	// an error; the rule evaluator will report every item-level check as
	// "0 rows checked," which is itself the finding.
	src.exhausted = true
	src.dec = dec
	return src, nil
}

func (s *jsonSource) Format() Format     { return FormatJSON }
func (s *jsonSource) Metadata() Metadata { return s.meta }

func (s *jsonSource) Next() (Row, error) {
	for len(s.pending) == 0 {
		if s.exhausted {
			return Row{}, ErrDone
		}
		if !s.dec.More() {
			if _, err := s.dec.Token(); err != nil && err != io.EOF {
				return Row{}, &ParseError{Format: FormatJSON, Err: fmt.Errorf("closing standard_charge_information array: %w", err)}
			}
			s.exhausted = true
			return Row{}, ErrDone
		}
		var item jsonItem
		if err := s.dec.Decode(&item); err != nil {
			return Row{}, &ParseError{Format: FormatJSON, Line: s.itemIndex + 1, Err: fmt.Errorf("decoding standard_charge_information item %d: %w", s.itemIndex+1, err)}
		}
		s.itemIndex++
		s.pending = flattenItem(s.itemIndex, item)
	}

	row := s.pending[0]
	s.pending = s.pending[1:]
	return row, nil
}

// flattenItem expands one standard_charge_information item — which can
// carry several settings (standard_charges), each with several payers
// (payers_information) — into one Row per (item, setting, payer) triple,
// the same granularity as the CSV tall format's one-row-per-payer layout.
// An item with no standard_charges, or a charge with no payers_information,
// still produces one Row so a genuinely missing charge/payer block shows up
// as a finding rather than silently vanishing.
func flattenItem(itemIndex int64, item jsonItem) []Row {
	var codes []Code
	for _, c := range item.CodeInformation {
		codes = append(codes, Code{Value: c.Code, Type: c.Type})
	}

	if len(item.StandardCharges) == 0 {
		return []Row{{LineNumber: itemIndex, Description: item.Description, Codes: codes}}
	}

	var rows []Row
	for _, sc := range item.StandardCharges {
		base := Row{
			LineNumber:      itemIndex,
			Description:     item.Description,
			Codes:           codes,
			Setting:         sc.Setting,
			GrossCharge:     sc.GrossCharge,
			DiscountedCash:  sc.DiscountedCash,
			DeidentifiedMin: sc.Minimum,
			DeidentifiedMax: sc.Maximum,
		}
		if len(sc.PayersInformation) == 0 {
			rows = append(rows, base)
			continue
		}
		for _, p := range sc.PayersInformation {
			row := base
			row.PayerName = p.PayerName
			row.PlanName = p.PlanName
			row.NegotiatedDollar = p.StandardChargeDollar
			row.NegotiatedPercentage = p.StandardChargePercent
			row.NegotiatedAlgorithm = p.StandardChargeAlgo
			row.MedianAmount = p.MedianAmount
			row.Pctl10Amount = p.Percentile10
			row.Pctl90Amount = p.Percentile90
			row.AllowedAmountCount = p.Count
			row.EstimatedAmount = p.EstimatedAmount
			rows = append(rows, row)
		}
	}
	return rows
}

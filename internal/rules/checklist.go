package rules

import (
	"fmt"
	"strings"

	"github.com/bobbylon127/mrfsentinel/internal/mrf"
)

// This checklist covers the fields this project's research (see the
// project's README.md Sources section) confirmed are required by name in
// CMS's own CSV/JSON data dictionaries and the CY2026 OPPS/ASC final rule
// fact sheet. It is deliberately not exhaustive — see ARCHITECTURE.md
// "Honest scoping" for what's left out and why (most notably: this checks
// that a Type 2 NPI is present and correctly *shaped*, not that it's a
// real, active NPI with the right taxonomy code, which would require a
// live lookup against NPPES, the National Plan and Provider Enumeration
// System — a real, addressable next step, not something worth faking here).

// hospitalChecks scores the file's hospital-level metadata: the things
// that are true (or not) about the file once, not per row.
func hospitalChecks(m mrf.Metadata) []CheckResult {
	return []CheckResult{
		checkNonEmpty("HOSP-NAME", "Hospital name is present", m.HospitalName),
		checkNonEmpty("HOSP-LAST-UPDATED", "Last-updated date is present", m.LastUpdatedOn),
		checkLicense(m),
		checkType2NPI(m),
		checkNonEmpty("CY26-ATTESTER-NAME", "CY2026: a named attester (CEO, president, or designated senior official) is present", m.AttesterName),
		checkAttestationConfirmed(m),
	}
}

// checkLicense scores whether the file carries hospital licensure
// information. It fails only when both the number and the state are
// missing: CMS permits a hospital that genuinely holds no license number to
// omit it, so the absence of that one field is not by itself a violation,
// while a file carrying neither field at all has clearly not addressed the
// requirement.
func checkLicense(m mrf.Metadata) CheckResult {
	if m.LicenseNumber == "" && m.LicenseState == "" {
		return CheckResult{
			RuleID:      "HOSP-LICENSE",
			Description: "Hospital licensure information is present",
			Passed:      false,
			Detail:      "no license number or state found (CMS allows omitting license_number only if the hospital genuinely has none — this file has neither field at all)",
		}
	}
	return CheckResult{
		RuleID:      "HOSP-LICENSE",
		Description: "Hospital licensure information is present",
		Passed:      true,
		Detail:      fmt.Sprintf("license %q, state %q", m.LicenseNumber, m.LicenseState),
	}
}

// checkType2NPI checks presence and a light structural shape (10 numeric
// digits) — it does NOT verify the NPI is real, active, or carries a
// hospital taxonomy code (27/28 prefix), which would require a live call
// to NPPES. See the package doc comment.
func checkType2NPI(m mrf.Metadata) CheckResult {
	if len(m.Type2NPIs) == 0 {
		return CheckResult{
			RuleID:      "CY26-TYPE2-NPI",
			Description: "CY2026: at least one Type 2 (organizational) NPI is present",
			Passed:      false,
			Detail:      "no type_2_npi values found",
		}
	}
	var malformed []string
	for _, npi := range m.Type2NPIs {
		if !isTenDigits(npi) {
			malformed = append(malformed, npi)
		}
	}
	if len(malformed) > 0 {
		return CheckResult{
			RuleID:      "CY26-TYPE2-NPI",
			Description: "CY2026: at least one Type 2 (organizational) NPI is present",
			Passed:      false,
			Detail:      fmt.Sprintf("found %d NPI(s), but not all are 10 digits: %s (this checks shape only, not that the NPI is real, active, or a hospital taxonomy — that needs an NPPES lookup)", len(m.Type2NPIs), strings.Join(malformed, ", ")),
		}
	}
	return CheckResult{
		RuleID:      "CY26-TYPE2-NPI",
		Description: "CY2026: at least one Type 2 (organizational) NPI is present",
		Passed:      true,
		Detail:      fmt.Sprintf("found %d NPI(s), all 10 digits (shape only — not verified against NPPES)", len(m.Type2NPIs)),
	}
}

// checkAttestationConfirmed scores the CY2026 attestation, distinguishing
// two failures that need different fixes: no attestation block at all (the
// file predates the CY2026 shape and needs the field added), versus a block
// that exists but was never affirmatively confirmed (the field is there and
// someone still has to sign it). Both are reported as failures, with the
// Detail text saying which — that distinction is the actionable part.
func checkAttestationConfirmed(m mrf.Metadata) CheckResult {
	if !m.AttestationFieldFound {
		return CheckResult{
			RuleID:      "CY26-ATTESTATION",
			Description: "CY2026: the file carries a confirmed attestation that the data is true, accurate, and complete",
			Passed:      false,
			Detail:      "no attestation block found at all",
		}
	}
	if !m.AttestationConfirmed {
		return CheckResult{
			RuleID:      "CY26-ATTESTATION",
			Description: "CY2026: the file carries a confirmed attestation that the data is true, accurate, and complete",
			Passed:      false,
			Detail:      "an attestation block is present but is not affirmatively confirmed (confirm_attestation is not true)",
		}
	}
	return CheckResult{
		RuleID:      "CY26-ATTESTATION",
		Description: "CY2026: the file carries a confirmed attestation that the data is true, accurate, and complete",
		Passed:      true,
		Detail:      "attestation confirmed",
	}
}

// isTenDigits reports whether s is exactly ten ASCII digits, the documented
// shape of an NPI. Written out rather than done with a regexp because it
// runs per NPI on every file and reads no worse; note it deliberately
// rejects non-ASCII digits, which strconv.Atoi would otherwise accept.
func isTenDigits(s string) bool {
	if len(s) != 10 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// itemRuleDef is one item-level rule: an id, a human-readable description,
// and a pure function scoring a single mrf.Row. Evaluate runs every rule
// in this table against every row it streams, so adding a rule to the
// checklist is exactly one entry here — nothing else in this package
// changes.
type itemRuleDef struct {
	id          string
	description string
	check       func(mrf.Row) bool
	pointer     func(mrf.Row) string
}

// defaultPointer renders the human-readable "here is where to look"
// breadcrumb recorded for a failing row — item number plus description. It
// is used by any rule in itemRules that does not supply its own pointer
// function, which today is all of them; the per-rule hook exists so a rule
// that fails for a field-specific reason can say so without changing how
// every other rule reports.
func defaultPointer(r mrf.Row) string {
	desc := r.Description
	if desc == "" {
		desc = "(no description)"
	}
	return fmt.Sprintf("item #%d: %s", r.LineNumber, desc)
}

// itemRules is the item-level checklist itself: every rule scored against
// every row of the file. The first six are the long-standing 45 CFR 180
// requirements; the last is new for CY2026. Adding a rule to this table is
// the entire change needed to add it to the product — Evaluate, the report
// types, the database schema and the report page are all driven by whatever
// is in here.
var itemRules = []itemRuleDef{
	{
		id:          "ITEM-DESCRIPTION",
		description: "Item has a description",
		check:       func(r mrf.Row) bool { return strings.TrimSpace(r.Description) != "" },
	},
	{
		id:          "ITEM-CODE",
		description: "Item has at least one billing code with its code type",
		check: func(r mrf.Row) bool {
			for _, c := range r.Codes {
				if strings.TrimSpace(c.Value) != "" && strings.TrimSpace(c.Type) != "" {
					return true
				}
			}
			return false
		},
	},
	{
		id:          "ITEM-GROSS-CHARGE",
		description: "Item reports a gross charge",
		check:       func(r mrf.Row) bool { return r.GrossCharge != nil },
	},
	{
		id:          "ITEM-DISCOUNTED-CASH",
		description: "Item reports a discounted cash price",
		check:       func(r mrf.Row) bool { return r.DiscountedCash != nil },
	},
	{
		id:          "ITEM-NEGOTIATED-CHARGE",
		description: "Item reports at least one payer-specific negotiated charge (dollar amount, percentage, or algorithm)",
		check:       func(r mrf.Row) bool { return r.HasAnyNegotiatedCharge() },
	},
	{
		id:          "ITEM-DEIDENTIFIED-MINMAX",
		description: "Item reports de-identified minimum and maximum negotiated charges",
		check:       func(r mrf.Row) bool { return r.DeidentifiedMin != nil && r.DeidentifiedMax != nil },
	},
	{
		id:          "CY26-PERCENTILES",
		description: "CY2026: item reports the dollar percentiles (median, 10th, 90th, and a count) that replace the pre-2026 single estimated allowed amount",
		check:       func(r mrf.Row) bool { return r.HasCY2026PercentileFields() },
	},
}

// Evaluate streams every row src produces, scoring it against every rule in
// itemRules, and combines that with the hospital-level checks into a
// Report. It stops early (returning whatever it accumulated) if src.Next
// returns anything other than mrf.ErrDone — see Report.ParseErr.
func Evaluate(src mrf.Source) Report {
	report := Report{
		Format:         string(src.Format()),
		HospitalName:   src.Metadata().HospitalName,
		HospitalChecks: hospitalChecks(src.Metadata()),
	}

	summaries := make([]ItemRuleSummary, len(itemRules))
	for i, rule := range itemRules {
		summaries[i] = ItemRuleSummary{RuleID: rule.id, Description: rule.description}
	}

	for {
		row, err := src.Next()
		if err != nil {
			if err != mrf.ErrDone {
				report.ParseErr = err.Error()
			}
			break
		}
		report.RowsProcessed++

		for i, rule := range itemRules {
			summaries[i].RowsChecked++
			if !rule.check(row) {
				pointerFn := rule.pointer
				if pointerFn == nil {
					pointerFn = defaultPointer
				}
				summaries[i].recordFailure(pointerFn(row))
			}
		}
	}

	report.ItemChecks = summaries
	return report
}

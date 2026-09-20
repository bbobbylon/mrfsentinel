package mrf

import (
	"strings"
	"testing"
)

// These are small, hand-built samples shaped like CMS's real MRF layouts
// (see json.go and csv.go's header comments for the exact field names and
// their sources) — not real hospital data, just enough to exercise every
// field this package reads.

const sampleJSON = `{
  "hospital_name": "Test General Hospital",
  "last_updated_on": "2026-06-01",
  "version": "3.0",
  "license_information": {"license_number": "12345", "state": "CA"},
  "type_2_npi": ["1234567890"],
  "attestation": {
    "attestation": "true, accurate, and complete",
    "confirm_attestation": true,
    "attester_name": "Jane Doe, CEO"
  },
  "standard_charge_information": [
    {
      "description": "Basic metabolic panel",
      "code_information": [{"code": "80048", "type": "CPT"}],
      "standard_charges": [
        {
          "gross_charge": 200.0,
          "discounted_cash": 150.0,
          "setting": "outpatient",
          "minimum": 50.0,
          "maximum": 300.0,
          "payers_information": [
            {
              "payer_name": "Acme Health",
              "plan_name": "PPO Gold",
              "standard_charge_dollar": 175.5,
              "median_amount": 170.0,
              "10th_percentile": 120.0,
              "90th_percentile": 250.0,
              "count": 42
            }
          ]
        }
      ]
    },
    {
      "description": "Missing everything item",
      "code_information": [],
      "standard_charges": []
    }
  ]
}`

func TestOpenJSON_MetadataAndRows(t *testing.T) {
	src, err := Open(strings.NewReader(sampleJSON))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if src.Format() != FormatJSON {
		t.Fatalf("Format() = %v, want %v", src.Format(), FormatJSON)
	}

	meta := src.Metadata()
	if meta.HospitalName != "Test General Hospital" {
		t.Errorf("HospitalName = %q", meta.HospitalName)
	}
	if meta.LicenseNumber != "12345" || meta.LicenseState != "CA" {
		t.Errorf("license = %q/%q, want 12345/CA", meta.LicenseNumber, meta.LicenseState)
	}
	if len(meta.Type2NPIs) != 1 || meta.Type2NPIs[0] != "1234567890" {
		t.Errorf("Type2NPIs = %v", meta.Type2NPIs)
	}
	if !meta.AttestationConfirmed || meta.AttesterName != "Jane Doe, CEO" {
		t.Errorf("attestation = confirmed=%v attester=%q", meta.AttestationConfirmed, meta.AttesterName)
	}

	row1, err := src.Next()
	if err != nil {
		t.Fatalf("Next() row1: %v", err)
	}
	if row1.Description != "Basic metabolic panel" {
		t.Errorf("row1.Description = %q", row1.Description)
	}
	if len(row1.Codes) != 1 || row1.Codes[0].Value != "80048" || row1.Codes[0].Type != "CPT" {
		t.Errorf("row1.Codes = %v", row1.Codes)
	}
	if row1.GrossCharge == nil || *row1.GrossCharge != 200.0 {
		t.Errorf("row1.GrossCharge = %v", row1.GrossCharge)
	}
	if row1.PayerName != "Acme Health" || row1.PlanName != "PPO Gold" {
		t.Errorf("row1 payer = %q/%q", row1.PayerName, row1.PlanName)
	}
	if !row1.HasAnyNegotiatedCharge() {
		t.Error("row1.HasAnyNegotiatedCharge() = false, want true")
	}
	if !row1.HasCY2026PercentileFields() {
		t.Error("row1.HasCY2026PercentileFields() = false, want true (median/10th/90th/count all set)")
	}

	row2, err := src.Next()
	if err != nil {
		t.Fatalf("Next() row2: %v", err)
	}
	if row2.Description != "Missing everything item" {
		t.Errorf("row2.Description = %q", row2.Description)
	}
	if row2.GrossCharge != nil {
		t.Errorf("row2.GrossCharge = %v, want nil (item had no standard_charges)", row2.GrossCharge)
	}
	if row2.HasAnyNegotiatedCharge() {
		t.Error("row2.HasAnyNegotiatedCharge() = true, want false")
	}

	if _, err := src.Next(); err != ErrDone {
		t.Fatalf("Next() after last row: err = %v, want ErrDone", err)
	}
}

func TestOpenJSON_RejectsNonObject(t *testing.T) {
	if _, err := Open(strings.NewReader(`[1,2,3]`)); err == nil {
		t.Fatal("Open() on a top-level JSON array: want error, got nil")
	}
}

const sampleCSVTall = "hospital_name,last_updated_on,version,license_number | CA,type_2_npi,attester_name,attestation\n" +
	"Test General Hospital,2026-06-01,3.0,12345,1234567890,\"Jane Doe, CEO\",\"true, accurate, and complete\"\n" +
	"description,code | 1,code | 1 | type,setting,standard_charge | gross,standard_charge | discounted_cash,payer_name,plan_name,standard_charge | negotiated_dollar,standard_charge | min,standard_charge | max,median_amount,10th_percentile,90th_percentile,count\n" +
	"Basic metabolic panel,80048,CPT,outpatient,200.0,150.0,Acme Health,PPO Gold,175.5,50.0,300.0,170.0,120.0,250.0,42\n" +
	"Comprehensive panel,80053,CPT,outpatient,,,Acme Health,PPO Gold,,,,,,,\n"

func TestOpenCSV_Tall_MetadataAndRows(t *testing.T) {
	src, err := Open(strings.NewReader(sampleCSVTall))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if src.Format() != FormatCSVTall {
		t.Fatalf("Format() = %v, want %v", src.Format(), FormatCSVTall)
	}

	meta := src.Metadata()
	if meta.HospitalName != "Test General Hospital" {
		t.Errorf("HospitalName = %q", meta.HospitalName)
	}
	if meta.LicenseNumber != "12345" || meta.LicenseState != "CA" {
		t.Errorf("license = %q/%q, want 12345/CA", meta.LicenseNumber, meta.LicenseState)
	}
	if len(meta.Type2NPIs) != 1 || meta.Type2NPIs[0] != "1234567890" {
		t.Errorf("Type2NPIs = %v", meta.Type2NPIs)
	}
	if !meta.AttestationConfirmed {
		t.Error("AttestationConfirmed = false, want true")
	}

	row1, err := src.Next()
	if err != nil {
		t.Fatalf("Next() row1: %v", err)
	}
	if row1.GrossCharge == nil || *row1.GrossCharge != 200.0 {
		t.Errorf("row1.GrossCharge = %v", row1.GrossCharge)
	}
	if !row1.HasCY2026PercentileFields() {
		t.Error("row1.HasCY2026PercentileFields() = false, want true")
	}

	row2, err := src.Next()
	if err != nil {
		t.Fatalf("Next() row2: %v", err)
	}
	if row2.GrossCharge != nil {
		t.Errorf("row2.GrossCharge = %v, want nil (blank cell)", row2.GrossCharge)
	}
	if row2.HasAnyNegotiatedCharge() {
		t.Error("row2.HasAnyNegotiatedCharge() = true, want false (blank negotiated-charge cells)")
	}

	if _, err := src.Next(); err != ErrDone {
		t.Fatalf("Next() after last row: err = %v, want ErrDone", err)
	}
}

const sampleCSVWide = "hospital_name,last_updated_on,version\n" +
	"Test General Hospital,2026-06-01,3.0\n" +
	"description,code | 1,code | 1 | type,setting,standard_charge | gross,standard_charge | discounted_cash,standard_charge | Acme Health | PPO Gold | negotiated_dollar,median_amount | Acme Health | PPO Gold,10th_percentile | Acme Health | PPO Gold,90th_percentile | Acme Health | PPO Gold,count | Acme Health | PPO Gold\n" +
	"Basic metabolic panel,80048,CPT,outpatient,200.0,150.0,175.5,170.0,120.0,250.0,42\n"

func TestOpenCSV_Wide_DetectsFormatAndAggregates(t *testing.T) {
	src, err := Open(strings.NewReader(sampleCSVWide))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if src.Format() != FormatCSVWide {
		t.Fatalf("Format() = %v, want %v", src.Format(), FormatCSVWide)
	}

	row, err := src.Next()
	if err != nil {
		t.Fatalf("Next(): %v", err)
	}
	if !row.HasAnyNegotiatedCharge() {
		t.Error("wide row HasAnyNegotiatedCharge() = false, want true (aggregate detection)")
	}
	if !row.HasCY2026PercentileFields() {
		t.Error("wide row HasCY2026PercentileFields() = false, want true (aggregate detection)")
	}
}

package legalidentity

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Registration remains an unverified owner-provided fact. It is not a legal
// form, tax election, or proof of registration with the named authority.
type Registration struct {
	CountryCode string `json:"country_code,omitempty"`
	Authority   string `json:"authority"`
	Number      string `json:"number"`
	Label       string `json:"label,omitempty"`
}

type Input struct {
	RegistrationStatus              string         `json:"registration_status,omitempty"`
	LegalName                       string         `json:"legal_name,omitempty"`
	FormationOrGoverningCountryCode string         `json:"formation_or_governing_country_code,omitempty"`
	LegalJurisdictionID             string         `json:"legal_jurisdiction_id,omitempty"`
	CustomLegalJurisdiction         string         `json:"custom_legal_jurisdiction,omitempty"`
	LegalFormID                     string         `json:"legal_form_id,omitempty"`
	CustomLegalForm                 string         `json:"custom_legal_form,omitempty"`
	OverlayIDs                      []string       `json:"overlay_ids,omitempty"`
	CustomOverlays                  []string       `json:"custom_overlays,omitempty"`
	TaxClassificationIDs            []string       `json:"tax_classification_ids,omitempty"`
	CustomTaxClassifications        []string       `json:"custom_tax_classifications,omitempty"`
	TaxResidenceCountryCodes        []string       `json:"tax_residence_country_codes,omitempty"`
	Registrations                   []Registration `json:"registrations,omitempty"`
}

// Snapshot retains labels and provenance at the time of selection. Persist and
// read it as-is: never re-run Validate when displaying or restoring history.
// Status describes the form selection (unknown, custom, or verified catalog
// selection), not verification of the subject or any owner-provided facts.
type Snapshot struct {
	Input
	CatalogVersion     string              `json:"catalog_version"`
	Status             string              `json:"status"`
	Country            *Country            `json:"country,omitempty"`
	Jurisdiction       *Jurisdiction       `json:"jurisdiction,omitempty"`
	LegalForm          *Form               `json:"legal_form,omitempty"`
	Overlays           []Overlay           `json:"overlays,omitempty"`
	TaxClassifications []TaxClassification `json:"tax_classifications,omitempty"`
}

// Validate normalizes an input and snapshots catalog selections. Empty input
// is intentionally valid for legacy Books, Personal Books, and unknown facts.
func Validate(in Input) (Snapshot, error) {
	var err error
	in.RegistrationStatus = strings.TrimSpace(in.RegistrationStatus)
	switch in.RegistrationStatus {
	case "", "registered", "not_registered", "unknown":
	default:
		return Snapshot{}, fmt.Errorf("choose a supported registration status")
	}
	in.FormationOrGoverningCountryCode, err = countryCode(in.FormationOrGoverningCountryCode)
	if err != nil {
		return Snapshot{}, fmt.Errorf("formation/governing country: %w", err)
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"legal name", &in.LegalName},
		{"legal jurisdiction ID", &in.LegalJurisdictionID},
		{"custom legal jurisdiction", &in.CustomLegalJurisdiction},
		{"legal form ID", &in.LegalFormID},
		{"custom legal form", &in.CustomLegalForm},
	} {
		*field.value, err = text(*field.value, 200)
		if err != nil {
			return Snapshot{}, fmt.Errorf("%s: %w", field.name, err)
		}
	}
	if in.LegalJurisdictionID != "" && in.CustomLegalJurisdiction != "" {
		return Snapshot{}, fmt.Errorf("choose a catalog jurisdiction or a custom jurisdiction")
	}
	if in.LegalFormID != "" && in.CustomLegalForm != "" {
		return Snapshot{}, fmt.Errorf("choose a catalog legal form or a custom legal form")
	}
	if (in.LegalJurisdictionID != "" || in.CustomLegalJurisdiction != "" || in.LegalFormID != "") && in.FormationOrGoverningCountryCode == "" {
		return Snapshot{}, fmt.Errorf("a formation/governing country is required for a jurisdiction or catalog legal form")
	}
	for _, list := range []struct {
		name  string
		value *[]string
	}{
		{"overlays", &in.OverlayIDs},
		{"custom overlays", &in.CustomOverlays},
		{"tax classifications", &in.TaxClassificationIDs},
		{"custom tax classifications", &in.CustomTaxClassifications},
		{"tax residence countries", &in.TaxResidenceCountryCodes},
	} {
		*list.value, err = textList(*list.value, 20)
		if err != nil {
			return Snapshot{}, fmt.Errorf("%s: %w", list.name, err)
		}
	}
	for i, code := range in.TaxResidenceCountryCodes {
		in.TaxResidenceCountryCodes[i], err = countryCode(code)
		if err != nil {
			return Snapshot{}, fmt.Errorf("tax residence country: %w", err)
		}
	}
	// Canonicalize after country-code normalization too, so US/us cannot record
	// duplicate residence facts. All facts are copied away from caller memory.
	in.TaxResidenceCountryCodes, err = textList(in.TaxResidenceCountryCodes, 20)
	if err != nil {
		return Snapshot{}, fmt.Errorf("tax residence countries: %w", err)
	}
	in.Registrations = slices.Clone(in.Registrations)
	if len(in.Registrations) > 20 {
		return Snapshot{}, fmt.Errorf("at most 20 registrations may be recorded")
	}
	for i := range in.Registrations {
		r := &in.Registrations[i]
		r.CountryCode, err = countryCode(r.CountryCode)
		if err != nil {
			return Snapshot{}, fmt.Errorf("registration country: %w", err)
		}
		for _, field := range []*string{&r.Authority, &r.Number, &r.Label} {
			*field, err = text(*field, 200)
			if err != nil {
				return Snapshot{}, fmt.Errorf("registration: %w", err)
			}
		}
		if r.Number == "" {
			return Snapshot{}, fmt.Errorf("registration number is required")
		}
	}

	c := catalog(&in.FormationOrGoverningCountryCode)
	out := Snapshot{Input: in, CatalogVersion: c.Version, Status: "unknown"}
	for _, country := range c.Countries {
		if country.Code == in.FormationOrGoverningCountryCode {
			out.Country = &country
			break
		}
	}
	if in.LegalJurisdictionID != "" {
		for _, j := range c.Jurisdictions {
			if j.ID == in.LegalJurisdictionID {
				out.Jurisdiction = &j
				break
			}
		}
		if out.Jurisdiction == nil {
			return Snapshot{}, fmt.Errorf("unknown catalog jurisdiction; use a custom jurisdiction label")
		}
		if out.Jurisdiction.CountryCode != in.FormationOrGoverningCountryCode {
			return Snapshot{}, fmt.Errorf("legal jurisdiction does not match formation/governing country")
		}
	}
	if in.CustomLegalForm != "" {
		out.Status = "custom"
	}
	if in.LegalFormID != "" {
		for _, f := range c.Forms {
			if f.ID == in.LegalFormID {
				out.LegalForm = &f
				break
			}
		}
		if out.LegalForm == nil {
			return Snapshot{}, fmt.Errorf("unknown catalog legal form; use a custom legal form label")
		}
		if out.LegalForm.CountryCode != in.FormationOrGoverningCountryCode || !slices.Contains(out.LegalForm.JurisdictionIDs, in.LegalJurisdictionID) {
			return Snapshot{}, fmt.Errorf("legal form is not in the catalog for the selected country and jurisdiction")
		}
		out.Status = "verified"
	}
	for _, id := range in.OverlayIDs {
		var found *Overlay
		for _, overlay := range c.Overlays {
			if overlay.ID == id {
				found = &overlay
				break
			}
		}
		if found == nil {
			return Snapshot{}, fmt.Errorf("unknown catalog overlay; use a custom overlay label")
		}
		if found.CountryCode != in.FormationOrGoverningCountryCode || !slices.Contains(found.LegalFormIDs, in.LegalFormID) {
			return Snapshot{}, fmt.Errorf("overlay is not in the catalog for the selected legal form")
		}
		out.Overlays = append(out.Overlays, *found)
	}
	groups := make(map[string]bool)
	for _, id := range in.TaxClassificationIDs {
		var found *TaxClassification
		for _, tax := range c.TaxClassifications {
			if tax.ID == id {
				found = &tax
				break
			}
		}
		if found == nil {
			return Snapshot{}, fmt.Errorf("unknown catalog tax classification; use a custom tax classification label")
		}
		if found.CountryCode != in.FormationOrGoverningCountryCode {
			return Snapshot{}, fmt.Errorf("tax classification is not in the catalog for the selected formation/governing country")
		}
		if found.ExclusiveGroup != "" {
			if groups[found.ExclusiveGroup] {
				return Snapshot{}, fmt.Errorf("conflicting tax classifications")
			}
			groups[found.ExclusiveGroup] = true
		}
		out.TaxClassifications = append(out.TaxClassifications, *found)
	}
	return out, nil
}

func text(value string, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit || strings.ContainsFunc(value, unicode.IsControl) {
		return "", fmt.Errorf("use at most %d characters without control characters", limit)
	}
	return value, nil
}

func textList(values []string, limit int) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("at most %d values may be recorded", limit)
	}
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, len(values))
	for i, value := range values {
		var err error
		out[i], err = text(value, 200)
		if err != nil {
			return nil, err
		}
		if out[i] == "" {
			return nil, fmt.Errorf("empty values are not allowed")
		}
	}
	slices.Sort(out)
	for i := 1; i < len(out); i++ {
		if out[i] == out[i-1] {
			return nil, fmt.Errorf("duplicate values are not allowed")
		}
	}
	return out, nil
}

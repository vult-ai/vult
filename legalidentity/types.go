// Package legalidentity provides pinned descriptive reference data. It does not
// verify a person, registration, election, eligibility or tax treatment.
package legalidentity

import (
	"errors"
	"strings"
)

const CatalogVersion = "2026-10-31.2"
const CheckedDate = "2026-10-25"

type Source struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Reference   string `json:"reference,omitempty"`
	CheckedDate string `json:"checked_date"`
}

type Country struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// ID is a legal jurisdiction identifier. Only SubdivisionCode claims to be an
// ISO subdivision code; federal Canada and England & Wales have no such code.
type Jurisdiction struct {
	ID              string   `json:"id"`
	CountryCode     string   `json:"country_code"`
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	SubdivisionCode string   `json:"subdivision_code,omitempty"`
	Sources         []Source `json:"sources"`
}

type Form struct {
	ID               string   `json:"id"`
	ELFCode          string   `json:"elf_code,omitempty"`
	CountryCode      string   `json:"country_code"`
	JurisdictionIDs  []string `json:"jurisdiction_ids"`
	LocalName        string   `json:"local_name"`
	EnglishName      string   `json:"english_name"`
	Aliases          []string `json:"aliases,omitempty"`
	Family           string   `json:"family"`
	Status           string   `json:"status"`
	LegalPersonality *bool    `json:"legal_personality"`
	Registration     string   `json:"registration,omitempty"`
	Notes            string   `json:"notes,omitempty"`
	EffectiveFrom    string   `json:"effective_from,omitempty"`
	EffectiveTo      string   `json:"effective_to,omitempty"`
	VerifiedAsOf     string   `json:"verified_as_of"`
	Sources          []Source `json:"sources"`
}

type Overlay struct {
	ID           string   `json:"id"`
	CountryCode  string   `json:"country_code"`
	Name         string   `json:"name"`
	LegalFormIDs []string `json:"legal_form_ids"`
	Notes        string   `json:"notes,omitempty"`
	Sources      []Source `json:"sources"`
}

type TaxClassification struct {
	ID             string   `json:"id"`
	CountryCode    string   `json:"country_code"`
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	ExclusiveGroup string   `json:"exclusive_group,omitempty"`
	Notes          string   `json:"notes,omitempty"`
	Sources        []Source `json:"sources"`
}

// Registry is a pinned descriptive subset, never an exhaustive availability claim.
type Registry struct {
	Version            string              `json:"version"`
	CheckedDate        string              `json:"checked_date"`
	Countries          []Country           `json:"countries"`
	Jurisdictions      []Jurisdiction      `json:"jurisdictions"`
	Forms              []Form              `json:"forms"`
	Overlays           []Overlay           `json:"overlays"`
	TaxClassifications []TaxClassification `json:"tax_classifications"`
}

func countryCode(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	// Unseeded countries are retained, not presented as catalog-verified. Do not
	// silently rewrite UK to GB or CA to a US subdivision.
	if len(value) != 2 || value[0] < 'A' || value[0] > 'Z' || value[1] < 'A' || value[1] > 'Z' {
		return "", errors.New("use a two-letter country code")
	}
	if value == "UK" {
		return "", errors.New("use GB for United Kingdom")
	}
	return value, nil
}

package legalidentity

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
)

// GLEIF public reference data, CC0; versions, input hashes and reproduction
// command are pinned in scripts/legal-identity/generate-reference.py.
//
//go:embed reference.json
var referenceJSON []byte

type referenceForm struct {
	Code, Country, Jurisdiction, Name string
	Aliases                           []string
}

var referenceData = func() struct {
	Jurisdictions []Jurisdiction
	Forms         []referenceForm
} {
	var data struct {
		Jurisdictions []Jurisdiction
		Forms         []referenceForm
	}
	if err := json.Unmarshal(referenceJSON, &data); err != nil {
		panic("invalid pinned legal identity reference: " + err.Error())
	}
	return data
}()

// Only exact reviewed form matches reuse existing identifiers. Broader upstream
// entries remain distinct (e.g. a corporation versus a stock corporation).
var existingELF = map[string]string{
	"NB58": "us-wy-llc",
	"UDLA": "ca-federal-business-corporation",
	"H0PO": "gb-private-limited-shares",
	"4GJI": "gb-private-limited-guarantee",
	"7T8N": "gb-ew-cio",
	"STX7": "gb-scotland-scio",
	"3BJG": "pl-spolka-partnerska",
	"629I": "pl-spolka-jawna",
	"BSJT": "pl-spolka-komandytowa",
	"OMX0": "pl-spolka-komandytowo-akcyjna",
	"O7XB": "pl-spolka-z-oo",
	"B21W": "pl-psa",
	"FJ0E": "pl-sa",
	"96XK": "pl-fundacja",
	"8TOF": "pl-spoldzielnia",
	"YLZL": "pl-spolka-cywilna",
}

func referenceSource(forms bool) Source {
	if forms {
		return Source{Title: "GLEIF ISO 20275 Entity Legal Forms", URL: "https://www.gleif.org/en/lei-data/code-lists/iso-20275-entity-legal-forms-code-list", Reference: "ELF v1.6, 2026-02-19; active codes; native names and aliases", CheckedDate: "2026-10-31"}
	}
	return Source{Title: "GLEIF Accepted Legal Jurisdictions", URL: "https://www.gleif.org/en/lei-data/code-lists/gleif-accepted-legal-jurisdictions-code-list", Reference: "v1.5, September 2023; legal formation jurisdictions, not all administrative subdivisions", CheckedDate: "2026-10-31"}
}

func extendReference(registry Registry, country *string) Registry {
	known := make(map[string]bool, len(registry.Jurisdictions))
	for _, item := range registry.Jurisdictions {
		known[item.ID] = true
	}
	for _, item := range referenceData.Jurisdictions {
		if !known[item.ID] {
			item.Sources = []Source{referenceSource(false)}
			registry.Jurisdictions = append(registry.Jurisdictions, item)
		}
	}
	slices.SortFunc(registry.Jurisdictions, func(a, b Jurisdiction) int {
		if a.CountryCode != b.CountryCode {
			return strings.Compare(a.CountryCode, b.CountryCode)
		}
		return strings.Compare(a.Name, b.Name)
	})
	for _, item := range referenceData.Forms {
		if country != nil && item.Country != *country {
			continue
		}
		// CIC remains a designation of an underlying company in Vult's contract.
		if item.Code == "17R0" {
			continue
		}
		if id := existingELF[item.Code]; id != "" {
			for i := range registry.Forms {
				f := &registry.Forms[i]
				if f.ID != id {
					continue
				}
				f.ELFCode = item.Code
				for _, alias := range append([]string{item.Name}, item.Aliases...) {
					if alias != f.LocalName && !slices.Contains(f.Aliases, alias) {
						f.Aliases = append(f.Aliases, alias)
					}
				}
				// The existing Companies House forms are reviewed across all three
				// UK jurisdictions. The upstream national choice is additional.
				if item.Code == "H0PO" || item.Code == "4GJI" {
					f.JurisdictionIDs = append(f.JurisdictionIDs, "GB")
				}
				f.Sources = append(f.Sources, referenceSource(true))
				break
			}
			continue
		}
		registry.Forms = append(registry.Forms, Form{
			ID: "elf-" + item.Code, ELFCode: item.Code, CountryCode: item.Country,
			JurisdictionIDs: []string{item.Jurisdiction}, LocalName: item.Name,
			// No invented translation, legal personality or tax equivalence.
			EnglishName: item.Name, Aliases: slices.Clone(item.Aliases), Family: "unclassified",
			Status: "active_at_review", VerifiedAsOf: "2026-10-31", Sources: []Source{referenceSource(true)},
		})
	}
	if country != nil {
		registry.Forms = slices.DeleteFunc(registry.Forms, func(f Form) bool { return f.CountryCode != *country })
	}
	return registry
}

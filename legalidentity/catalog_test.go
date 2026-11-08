package legalidentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestCatalogReferencesStayUniqueAndScoped(t *testing.T) {
	digest := sha256.Sum256(referenceJSON)
	if hex.EncodeToString(digest[:]) != "6212453f2537c606f8c56f96d239f0cf23b467baa22d949ce8bb2c459596dc45" {
		t.Fatal("generated reference differs from the pinned artifact")
	}
	catalog := Catalog()
	if catalog.Version != CatalogVersion || len(catalog.Countries) != 250 || len(catalog.Jurisdictions) != 322 || len(catalog.Forms) != 3450 {
		t.Fatal("pinned reference inventory changed without a version decision")
	}
	countries, jurisdictions, forms := map[string]bool{}, map[string]string{}, map[string]bool{}
	for _, c := range catalog.Countries {
		if c.Code == "" || c.Name == "" || countries[c.Code] {
			t.Fatal("invalid or repeated country reference", c.Code)
		}
		countries[c.Code] = true
	}
	for _, j := range catalog.Jurisdictions {
		if j.ID == "" || j.Name == "" || !countries[j.CountryCode] || jurisdictions[j.ID] != "" || len(j.Sources) == 0 {
			t.Fatal("invalid jurisdiction reference", j.ID)
		}
		jurisdictions[j.ID] = j.CountryCode
	}
	for _, f := range catalog.Forms {
		if f.ID == "" || f.LocalName == "" || forms[f.ID] || !countries[f.CountryCode] || len(f.JurisdictionIDs) == 0 || len(f.Sources) == 0 || f.VerifiedAsOf == "" {
			t.Fatal("invalid form reference", f.ID)
		}
		forms[f.ID] = true
		for _, id := range f.JurisdictionIDs {
			if jurisdictions[id] != f.CountryCode {
				t.Fatal("form crosses its recorded jurisdiction", f.ID, id)
			}
		}
		for _, s := range f.Sources {
			if s.Title == "" || s.URL == "" || s.CheckedDate == "" {
				t.Fatal("reference lost its provenance", f.ID)
			}
		}
		if f.ELFCode == "8888" || f.ELFCode == "9999" || f.ELFCode == "17R0" {
			t.Fatal("reserved or designation code became a legal form", f.ID)
		}
	}
	for _, o := range catalog.Overlays {
		if !countries[o.CountryCode] || len(o.Sources) == 0 {
			t.Fatal("invalid designation", o.ID)
		}
		for _, id := range o.LegalFormIDs {
			if !forms[id] {
				t.Fatal("designation refers to a missing form", id)
			}
		}
	}
	for _, tax := range catalog.TaxClassifications {
		if !countries[tax.CountryCode] || len(tax.Sources) == 0 {
			t.Fatal("invalid tax reference", tax.ID)
		}
	}
	for _, country := range []string{"US", "GB", "CA", "PL", "JP", "DE", "", "ZZ"} {
		filtered, err := CatalogForCountry(country)
		if err != nil || len(filtered.Countries) != len(catalog.Countries) || len(filtered.Jurisdictions) != len(catalog.Jurisdictions) {
			t.Fatal("filter lost navigation choices", country, err)
		}
		for _, f := range filtered.Forms {
			if f.CountryCode != country {
				t.Fatal("country filter leaked another country's form", country, f.ID)
			}
		}
		if (country == "" || country == "ZZ") && len(filtered.Forms) != 0 {
			t.Fatal("unknown country acquired a form", country)
		}
	}
}

func TestCatalogCallersCannotChangePinnedReferenceData(t *testing.T) {
	original := Catalog()
	want, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	original.Countries[0].Name = "Caller mutation"
	original.Jurisdictions[0].Name = "Caller mutation"
	original.Jurisdictions[0].Sources[0].URL = "https://example.invalid/changed"
	for i := range original.Forms {
		f := &original.Forms[i]
		f.JurisdictionIDs[0] = "changed"
		f.Sources[0].Title = "Caller mutation"
		if len(f.Aliases) > 0 {
			f.Aliases[0] = "Caller mutation"
		}
		if f.LegalPersonality != nil {
			*f.LegalPersonality = !*f.LegalPersonality
		}
	}
	original.Overlays[0].LegalFormIDs[0] = "changed"
	original.TaxClassifications[0].Sources[0].URL = "https://example.invalid/changed"
	got, err := json.Marshal(Catalog())
	if err != nil || !bytes.Equal(want, got) {
		t.Fatal("caller changed a later catalog result", err)
	}
	if len(got) > 4<<20 {
		t.Fatal("reference response exceeds its bounded size")
	}
}

func TestCatalogCountryFilterDoesNotGuessAliases(t *testing.T) {
	for _, value := range []string{"US-CA", "UK", "U", "USA", "日本", "U\x00"} {
		if _, err := CatalogForCountry(value); err == nil {
			t.Fatal("ambiguous country filter accepted", value)
		}
	}
	c, err := CatalogForCountry(" us ")
	if err != nil || len(c.Forms) == 0 {
		t.Fatal("country normalization lost its reference", err)
	}
	for _, f := range c.Forms {
		if f.CountryCode != "US" {
			t.Fatal("normalization changed the requested country")
		}
	}
}

package legalidentity

// Catalog returns independent data on every call so a UI, caller, or saved
// snapshot cannot mutate later selections. IDs are permanent; labels and source
// revisions require a new CatalogVersion. Effective dates below refer to form
// availability only when verified, never to the subject's incorporation date.
func Catalog() Registry {
	return catalog(nil)
}

// CatalogForCountry keeps the country/jurisdiction choices available while
// sending only the selected country's forms. Empty selects no forms.
func CatalogForCountry(country string) (Registry, error) {
	code, err := countryCode(country)
	if err != nil {
		return Registry{}, err
	}
	return catalog(&code), nil
}

func catalog(country *string) Registry {
	wy := source("Wyoming Limited Liability Company Act", "https://wyoleg.gov/statutes/compress/title17.pdf", "W.S. 17-29-104, 105, 108, 201")
	de := source("Delaware General Corporation Law", "https://delcode.delaware.gov/title8/c001/sc001/index.html", "8 Del. C. §§101, 102, 106")
	ca := source("Canada Business Corporations Act", "https://laws-lois.justice.gc.ca/eng/acts/C-44/page-2.html", "sections 5–9; federal incorporation")
	caCapacity := source("Canada Business Corporations Act: Capacity", "https://laws-lois.justice.gc.ca/eng/acts/C-44/page-3.html", "section 15; corporate capacity and rights")
	caScope := source("Federal and provincial/territorial incorporation", "https://www.canada.ca/en/services/business/start/register-with-gov/register-corp/register-corp-fed-or-prov.html", "separate incorporation regimes")
	on := source("Ontario: Incorporating a Business Corporation", "https://forms.mgcs.gov.on.ca/dataset/abc650b9-3b00-42ea-bdcd-ac455e58c9ed/resource/d414fced-f60e-472f-862a-f21530037776/download/on00224e.pdf", "Business Corporations Act notice, effective 1 February 2025")
	gb := source("Companies House: Incorporation and names", "https://www.gov.uk/government/publications/incorporation-and-names/incorporation-and-names", "sections 1.1, 1.3, 1.13")
	cic := source("Regulator of Community Interest Companies: Guidance", "https://www.gov.uk/government/publications/community-interest-companies-how-to-form-a-cic/community-interest-companies-guidance-chapters", "What is a CIC?; Limited by Shares / Limited by Guarantee")
	cio := source("HM Land Registry: Charitable incorporated organisations", "https://www.gov.uk/government/publications/charitable-incorporated-organisations-pg14a/practice-guide-14a-charitable-incorporated-organisations", "sections 1.1–1.2; England and Wales")
	scio := source("OSCR: What is a SCIO?", "https://www.oscr.org.uk/managing-a-charity/responsibilities-of-scios-and-other-legal-forms/scios-a-guide/1-what-is-a-scio/", "sections 1.1–1.3; Scottish corporate body")
	plBusiness := source("Polish Ministry of Justice: Business activity in Poland", "https://www.gov.pl/web/sprawiedliwosc/ogolne-informacje-o-prowadzeniu-dzialalnosci-gospodarczej-w-polsce", "CEIDG natural persons and civil partnership participants")
	plCapacity := source("Statistics Poland: Organisational units without legal personality", "https://stat.gov.pl/metainformacje/slownik-pojec/pojecia-stosowane-w-statystyce-publicznej/816,pojecie.html", "civil partnership; commercial partnerships with legal capacity")
	ksh := source("Polish Commercial Companies Code", "https://eli.gov.pl/api/acts/DU/2024/18/text/U/D20240018Lj.pdf", "codified text 3 June 2026, articles 1, 4, 8, 12; commercial forms and legal personality")
	foundation := source("Polish Foundations Act", "https://eli.gov.pl/api/acts/DU/2023/166/text/U/D20230166Lj.pdf", "codified text 25 March 2026, article 7; KRS registration and legal personality")
	familyFoundation := source("Polish Family Foundation Act", "https://eli.gov.pl/eli/DU/2023/326/ogl", "articles 2–4, 23; distinct form and family-foundation register")
	association := source("Polish Law on Associations", "https://eli.gov.pl/api/acts/DU/2020/2261/text/U/D20202261Lj.pdf", "codified text 25 March 2026, article 17; registered association, distinct from ordinary association")
	cooperative := source("Polish Cooperative Law", "https://eli.gov.pl/api/acts/DU/2024/593/text/U/D20240593Lj.pdf", "codified text 1 December 2025, articles 7, 11; KRS registration and legal personality")
	irsS := source("IRS: S corporations", "https://www.irs.gov/businesses/small-businesses-self-employed/s-corporations", "federal tax election; no eligibility determination here")
	irsLLC := source("IRS: Domestic LLC tax classification", "https://www.irs.gov/faqs/small-business-self-employed-other-business/entities/entities-3", "LLC corporation classification and Form 2553 election")
	irsC := source("IRS: Forming a corporation", "https://www.irs.gov/businesses/small-businesses-self-employed/forming-a-corporation", "C corporation federal tax treatment")

	registry := Registry{
		Version: CatalogVersion, CheckedDate: "2026-10-31",
		Countries: countries(),
		Jurisdictions: []Jurisdiction{
			{ID: "US-WY", CountryCode: "US", Name: "Wyoming", Kind: "state", SubdivisionCode: "US-WY", Sources: []Source{wy}},
			{ID: "US-DE", CountryCode: "US", Name: "Delaware", Kind: "state", SubdivisionCode: "US-DE", Sources: []Source{de}},
			{ID: "ca-federal", CountryCode: "CA", Name: "Canada (federal)", Kind: "federal", Sources: []Source{ca, caScope}},
			{ID: "CA-ON", CountryCode: "CA", Name: "Ontario", Kind: "province", SubdivisionCode: "CA-ON", Sources: []Source{on}},
			{ID: "gb-england-wales", CountryCode: "GB", Name: "England and Wales", Kind: "legal_jurisdiction", Sources: []Source{gb, cio}},
			{ID: "gb-scotland", CountryCode: "GB", Name: "Scotland", Kind: "legal_jurisdiction", SubdivisionCode: "GB-SCT", Sources: []Source{gb, scio}},
			{ID: "gb-northern-ireland", CountryCode: "GB", Name: "Northern Ireland", Kind: "legal_jurisdiction", SubdivisionCode: "GB-NIR", Sources: []Source{gb}},
			{ID: "PL", CountryCode: "PL", Name: "Poland", Kind: "national", Sources: []Source{ksh}},
		},
		Forms: []Form{
			form("us-wy-llc", "US", []string{"US-WY"}, "Limited liability company", "Limited liability company (Wyoming)", []string{"LLC", "L.L.C."}, "limited_liability_company", truth(true), "Wyoming Secretary of State", "State legal form; federal tax treatment is separate.", wy),
			form("us-de-stock-corporation", "US", []string{"US-DE"}, "Stock corporation", "Stock corporation (Delaware)", []string{"Corporation", "Corp.", "Inc."}, "corporation", truth(true), "Delaware Division of Corporations", "Stock corporation under the DGCL; neither C nor S is a state legal form.", de),
			form("ca-federal-business-corporation", "CA", []string{"ca-federal"}, "Business corporation (federal)", "Business corporation (Canada federal)", []string{"CBCA corporation"}, "corporation", truth(true), "Corporations Canada", "Federal incorporation is separate from extra-provincial registrations.", ca, caCapacity, caScope),
			form("ca-on-business-corporation", "CA", []string{"CA-ON"}, "Business corporation (Ontario)", "Business corporation (Ontario)", nil, "corporation", nil, "Ontario Business Registry", "Verified Ontario formation subset; no availability claim for other provinces or professions.", on),
			form("gb-private-limited-shares", "GB", []string{"gb-england-wales", "gb-scotland", "gb-northern-ireland"}, "Private company limited by shares", "Private company limited by shares", []string{"Ltd", "Limited"}, "company", truth(true), "Companies House", "Shares and guarantee are separate forms. CIC is an optional designation.", gb),
			form("gb-private-limited-guarantee", "GB", []string{"gb-england-wales", "gb-scotland", "gb-northern-ireland"}, "Private company limited by guarantee", "Private company limited by guarantee", []string{"Company limited by guarantee"}, "company", truth(true), "Companies House", "No share capital; selecting this form does not establish charity status.", gb),
			form("gb-ew-cio", "GB", []string{"gb-england-wales"}, "Charitable incorporated organisation", "Charitable incorporated organisation (England and Wales)", []string{"CIO"}, "charitable_incorporated_organisation", truth(true), "Charity Commission for England and Wales", "Separate framework from Companies Act companies and Scottish SCIOs.", cio),
			form("gb-scotland-scio", "GB", []string{"gb-scotland"}, "Scottish charitable incorporated organisation", "Scottish charitable incorporated organisation", []string{"SCIO"}, "charitable_incorporated_organisation", truth(true), "Office of the Scottish Charity Regulator (OSCR)", "Scottish form. Selection is not verification of entry in the charity register.", scio),
			form("pl-jdg", "PL", []string{"PL"}, "jednoosobowa działalność gospodarcza", "Individual business activity (Poland)", []string{"JDG"}, "individual_activity", truth(false), "CEIDG (individual entrepreneur)", "Accounting activity of a natural person; no distinct legal person is created.", plBusiness),
			form("pl-spolka-cywilna", "PL", []string{"PL"}, "spółka cywilna", "Civil-law partnership (Poland)", []string{"s.c."}, "civil_partnership", truth(false), "Participants' registrations; not incorporation of the partnership", "A contractual civil-law partnership, not an incorporated company.", plBusiness, plCapacity),
			form("pl-spolka-jawna", "PL", []string{"PL"}, "spółka jawna", "Registered partnership (Poland)", []string{"sp.j."}, "commercial_partnership", truth(false), "Krajowy Rejestr Sądowy (KRS)", "Has statutory legal capacity; lack of legal personality does not mean lack of capacity.", ksh, plCapacity),
			form("pl-spolka-partnerska", "PL", []string{"PL"}, "spółka partnerska", "Professional partnership (Poland)", []string{"sp.p."}, "commercial_partnership", truth(false), "Krajowy Rejestr Sądowy (KRS)", "Profession-specific eligibility is not determined by this catalog.", ksh, plCapacity),
			form("pl-spolka-komandytowa", "PL", []string{"PL"}, "spółka komandytowa", "Limited partnership (Poland)", []string{"sp.k."}, "commercial_partnership", truth(false), "Krajowy Rejestr Sądowy (KRS)", "Has statutory legal capacity; not a US entity equivalence.", ksh, plCapacity),
			form("pl-spolka-komandytowo-akcyjna", "PL", []string{"PL"}, "spółka komandytowo-akcyjna", "Limited joint-stock partnership (Poland)", []string{"S.K.A."}, "commercial_partnership", truth(false), "Krajowy Rejestr Sądowy (KRS)", "A Polish commercial partnership, distinct from a joint-stock company.", ksh, plCapacity),
			form("pl-spolka-z-oo", "PL", []string{"PL"}, "spółka z ograniczoną odpowiedzialnością", "Limited liability company (Poland)", []string{"sp. z o.o.", "spółka z o.o."}, "capital_company", truth(true), "Krajowy Rejestr Sądowy (KRS)", "Legal personality upon registration; no US LLC or tax equivalence is implied.", ksh),
			form("pl-psa", "PL", []string{"PL"}, "prosta spółka akcyjna", "Simple joint-stock company (Poland)", []string{"PSA", "P.S.A."}, "capital_company", truth(true), "Krajowy Rejestr Sądowy (KRS)", "Legal personality upon registration.", ksh),
			form("pl-sa", "PL", []string{"PL"}, "spółka akcyjna", "Joint-stock company (Poland)", []string{"S.A."}, "capital_company", truth(true), "Krajowy Rejestr Sądowy (KRS)", "Legal personality upon registration.", ksh),
			form("pl-fundacja", "PL", []string{"PL"}, "fundacja", "Foundation (Poland)", nil, "foundation", truth(true), "Krajowy Rejestr Sądowy (KRS)", "Legal personality upon registration; separate from fundacja rodzinna.", foundation),
			form("pl-fundacja-rodzinna", "PL", []string{"PL"}, "fundacja rodzinna", "Family foundation (Poland)", []string{"F.R."}, "family_foundation", truth(true), "Rejestr fundacji rodzinnych", "Distinct statutory legal person upon registration; not an ordinary fundacja or an Anglo-American trust.", familyFoundation),
			form("pl-stowarzyszenie-rejestrowe", "PL", []string{"PL"}, "stowarzyszenie (rejestrowe)", "Registered association (Poland)", []string{"stowarzyszenie"}, "registered_association", truth(true), "Krajowy Rejestr Sądowy (KRS)", "Registered form only; ordinary associations (stowarzyszenia zwykłe) are outside this seed.", association),
			form("pl-spoldzielnia", "PL", []string{"PL"}, "spółdzielnia", "Cooperative (Poland)", nil, "cooperative", truth(true), "Krajowy Rejestr Sądowy (KRS)", "Legal personality upon registration; specialized cooperative rules are not determined.", cooperative),
		},
		Overlays: []Overlay{
			{ID: "gb-cic", CountryCode: "GB", Name: "Community interest company (CIC)", LegalFormIDs: []string{"gb-private-limited-shares", "gb-private-limited-guarantee"}, Notes: "Designation of a qualifying limited company; selecting it does not establish eligibility or regulatory approval.", Sources: []Source{cic}},
		},
		TaxClassifications: []TaxClassification{
			{ID: "us-federal-s-corporation", CountryCode: "US", Name: "US federal S corporation election", Kind: "election", ExclusiveGroup: "us-federal-corporate-treatment", Notes: "Owner-reported election only. No Form 2553 filing, eligibility, acceptance, or state-tax treatment is determined.", Sources: []Source{irsS, irsLLC}},
			{ID: "us-federal-c-corporation", CountryCode: "US", Name: "US federal C corporation treatment", Kind: "classification", ExclusiveGroup: "us-federal-corporate-treatment", Notes: "Owner-reported federal treatment, separate from state formation. No tax computations or state-tax treatment.", Sources: []Source{irsC, irsLLC}},
		},
	}
	return extendReference(registry, country)
}

func source(title, url, reference string) Source {
	return Source{Title: title, URL: url, Reference: reference, CheckedDate: CheckedDate}
}

func truth(value bool) *bool { return &value }

func form(id, country string, jurisdictions []string, local, english string, aliases []string, family string, personality *bool, registration, notes string, sources ...Source) Form {
	return Form{
		ID: id, CountryCode: country, JurisdictionIDs: jurisdictions,
		LocalName: local, EnglishName: english, Aliases: aliases, Family: family, Status: "active_at_review",
		LegalPersonality: personality, Registration: registration, Notes: notes,
		VerifiedAsOf: CheckedDate, Sources: sources,
	}
}

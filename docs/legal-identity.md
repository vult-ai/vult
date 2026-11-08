# Legal identity reference data

The `legalidentity` package supplies versioned choices for countries,
jurisdictions, legal forms, designations and tax classifications. The bundled
reference is descriptive data as of its stated review dates. A choice does not
verify a person, registration, election, eligibility, tax residence or compliance.

Legal form, registration, tax classification and residence are separate facts.
A designation is not a substitute for the underlying legal form. Reference
coverage is not an exhaustive claim about which structures are available.
An unknown or custom description remains distinct from a catalog selection.

The browser catalog endpoint requires its ordinary session and rejects bearer
tokens. An optional country filter returns that country's forms while retaining
the country and jurisdiction choices needed for navigation. An empty filter
returns no forms. It never reads or writes financial records or accepts an
identity change.

## Pinned sources

Catalog version `2026-10-31.2` includes GLEIF public reference data retrieved on
31 October 2026:

- [ISO 20275 Entity Legal Forms](https://www.gleif.org/en/about-lei/code-lists/iso-20275-entity-legal-forms-code-list): version 1.6, dated 19 February 2026.
- [Accepted Legal Jurisdictions](https://www.gleif.org/en/about-lei/code-lists/gleif-accepted-legal-jurisdictions-code-list): version 1.5, September 2023.
- Country references from GLEIF's public API, with their input digest in
  `legalidentity/countries.go`.

GLEIF publishes its website data under [CC0](https://www.gleif.org/en/about/open-data/).

The generated `legalidentity/reference.json` digest is
`6212453f2537c606f8c56f96d239f0cf23b467baa22d949ce8bb2c459596dc45`.
Source hashes and generation rules are in
`scripts/legal-identity/generate-reference.py`. Pass it the matching upstream
ELF and jurisdiction CSV files; a different input digest is rejected. Runtime
never downloads or refreshes reference data.

Additional form descriptions retain their own source URLs and review dates.
Upstream entries without an established translation or legal-personality fact
keep the original label and an unknown value. A future reference revision must
have a new catalog version and preserve existing identifiers and saved labels.

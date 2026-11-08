#!/usr/bin/env python3
"""Generate pinned GLEIF reference data. Inputs are public upstream CSV files.

Usage: python3 scripts/legal-identity/generate-reference.py ELF.csv JURISDICTIONS.csv
Runtime never downloads reference data. Verify a new upstream revision and update
both hashes deliberately; existing Vult IDs and saved snapshots are independent.
"""
import csv
import hashlib
import io
import json
from pathlib import Path
import re
import sys

SOURCES = [
    ("c55edc421e49ce362457f772d6bfa41f5fc63ecaadea74db5735722625506ef4", "ELF v1.6 (2026-02-19)"),
    ("e0aef168b11ad30b8517366dc89b8eddb59c6628f15919140030ab1823170a49", "Accepted Legal Jurisdictions v1.5 (2023-09)"),
]
root = Path(__file__).resolve().parents[2]
inputs = []
for path, (digest, _) in zip(sys.argv[1:], SOURCES, strict=True):
    raw = Path(path).read_bytes()
    if hashlib.sha256(raw).hexdigest() != digest:
        raise SystemExit("Unexpected upstream digest; review the new reference revision first")
    inputs.append(list(csv.DictReader(io.StringIO(raw.decode("utf-8-sig")))))
forms, jurisdictions = inputs
countries = dict(re.findall(r'\{"([A-Z]{2})", "([^"]+)"\}', (root / "legalidentity/countries.go").read_text()))
aliases = {"CA": "ca-federal", "GB-SCT": "gb-scotland", "GB-NIR": "gb-northern-ireland"}
jurisdiction_rows = []
known = set()
for row in jurisdictions:
    code = row["Code"].strip()
    country = code.split("-")[0]
    if country not in countries:  # EU, UN and XX are not registration countries.
        continue
    ident = aliases.get(code, code)
    name = re.sub(r" \([^)]*\)$", "", row["Jurisdiction"].strip())
    kind = "national" if "-" not in code else "legal_jurisdiction"
    if code in {"US", "CA"}:
        name, kind = countries[country] + " (federal)", "federal"
    elif code == "GB":
        name = "United Kingdom (national forms)"
    elif country == "US":
        kind = "district" if code == "US-DC" else "state"
    elif country == "CA":
        kind = "territory" if code in {"CA-NT", "CA-NU", "CA-YT"} else "province"
    jurisdiction_rows.append([ident, country, name, kind, code if "-" in code else ""])
    known.add(code)

grouped = {}
for row in forms:
    code = row["ELF Code"].strip()
    country = row["Country Code (ISO 3166-1)"].strip()
    if row["ELF Status ACTV/INAC"] != "ACTV" or code in {"8888", "9999"}:
        continue
    jurisdiction = row["Country sub-division code (ISO 3166-2)"].strip() or country
    if country not in countries or jurisdiction not in known:
        raise SystemExit(f"Unmapped active reference: {code}/{country}/{jurisdiction}")
    entry = grouped.setdefault(code, {"country": country, "jurisdiction": aliases.get(jurisdiction, jurisdiction), "names": [], "aliases": []})
    if (entry["country"], entry["jurisdiction"]) != (country, aliases.get(jurisdiction, jurisdiction)):
        raise SystemExit(f"Conflicting ELF jurisdiction: {code}")
    name = row["Entity Legal Form name Local name"].strip()
    entry["names"].append(name)
    for column in ["Entity Legal Form name Transliterated name (per ISO 01-140-10)", "Abbreviations Local language", "Abbreviations transliterated"]:
        entry["aliases"].extend(x.strip() for x in row[column].split(";") if x.strip())
form_rows = []
for code, row in sorted(grouped.items()):
    local = row["names"][0]
    search = sorted(set(row["names"] + row["aliases"]) - {local})
    form_rows.append([code, row["country"], row["jurisdiction"], local, search])
output = {
    "jurisdictions": [dict(zip(["id", "country_code", "name", "kind", "subdivision_code"], row)) for row in jurisdiction_rows],
    "forms": [dict(zip(["code", "country", "jurisdiction", "name", "aliases"], row)) for row in form_rows],
}
path = root / "legalidentity/reference.json"
path.write_text(json.dumps(output, ensure_ascii=False, separators=(",", ":")) + "\n")
print(f"Generated {len(jurisdiction_rows)} jurisdictions, {len(form_rows)} active ELF codes, {len(set(row[1] for row in form_rows))} countries; {path.stat().st_size} bytes")

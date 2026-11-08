import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
const { activityBrand, parseEntryBrands, readEntryBrands } =
  await loadComponent("src/lib/activity-brands.ts");

const google = {
  id: "google",
  name: "Google",
  domain: "google.com",
  aliases: ["Google LLC"],
};
const catalog = [
  google,
  { id: "google-workspace", name: "Google Workspace", domain: "google.com" },
];
test("only an unambiguous typed catalog identity selects artwork", () => {
  for (const name of ["Google", "Ｇｏｏｇｌｅ", " Google   LLC "])
    assert.equal(activityBrand({ state: "proposed", name }, catalog), google);
  assert.equal(
    activityBrand({ state: "identified", brand_id: "google" }, catalog),
    google,
  );
  for (const brand of [
    undefined,
    { state: "unknown" },
    { state: "not_applicable" },
    { state: "identified", brand_id: "invented" },
    { state: "proposed", name: "GOOGLE *SVCS unrelated private description" },
    { state: "proposed", name: "Google", domain: "not-google.example" },
  ])
    assert.equal(activityBrand(brand, catalog), undefined);
  assert.equal(
    activityBrand({ state: "proposed", name: "Google" }, [
      google,
      { ...google, id: "ambiguous" },
    ]),
    undefined,
  );
  assert.equal(
    activityBrand({ state: "proposed", name: "Google Workspace" }, catalog).id,
    "google-workspace",
  );
});

test("brand metadata cannot substitute another entry or omit part of the page", () => {
  assert.equal(
    parseEntryBrands({ brands: [{ entry_id: "a", state: "unknown" }] }, [
      "a",
    ]).get("a").state,
    "unknown",
  );
  for (const brands of [
    [],
    [{ entry_id: "b", state: "unknown" }],
    [{ entry_id: "a", state: "identified", brand_id: "../../bad" }],
    [{ entry_id: "a", state: "proposed", name: "" }],
    [{ entry_id: "a", state: "unrecognized" }],
  ])
    assert.throws(() => parseEntryBrands({ brands }, ["a"]));
  assert.throws(() =>
    parseEntryBrands(
      {
        brands: [
          { entry_id: "a", state: "unknown" },
          { entry_id: "a", state: "unknown" },
        ],
      },
      ["a", "b"],
    ),
  );
});


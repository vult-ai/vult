import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";

const reference = JSON.parse(
  readFileSync(
    new URL("../../legalidentity/reference.json", import.meta.url),
  ),
);
const formsFor = (country, jurisdiction) =>
  reference.forms
    .filter((f) => f.country === country && f.jurisdiction === jurisdiction)
    .map((f) => ({
      id: f.code === "NB58" ? "us-wy-llc" : `elf-${f.code}`,
      elf_code: f.code,
      country_code: f.country,
      jurisdiction_ids: [f.jurisdiction],
      local_name: f.name,
      english_name: f.name,
      aliases: f.aliases,
      legal_personality: null,
      sources: [],
    }));

test("Wyoming starts with five familiar choices, preserves exact mappings and captures the missing sole proprietor honestly", async () => {
  const { businessStructureOptions } = await loadComponent(
    "src/components/onboarding/business-structure-options.ts",
  );
  const forms = formsFor("US", "US-WY");
  const result = businessStructureOptions(forms, "US", "US-WY", "", "");
  assert.deepEqual(
    result.common.map((x) => x.label),
    [
      "Sole proprietorship",
      "Limited liability company (LLC)",
      "Corporation",
      "Partnership",
      "Nonprofit corporation",
      "Another structure",
      "Not sure yet",
    ],
  );
  assert.equal(result.common[0].form, "custom");
  assert.equal(result.common[0].customForm, "Sole proprietorship");
  assert.equal(result.common[1].form, "us-wy-llc");
  assert.equal(result.common[2].form, "elf-9GXA");
  assert.equal(
    result.common[3].form,
    "custom",
    "a generic partnership never guesses LP/LLP",
  );
  assert.equal(result.hasMore, true);
  for (const form of forms)
    assert.ok(
      result.all.some((x) => x.id === form.id),
      `${form.local_name} remains reachable`,
    );
  assert.ok(
    !result.common.some((x) =>
      /bank|trust|foundation|credit union/i.test(x.label),
    ),
  );
});

test("curation is country-specific; broad, missing and ambiguous choices never pick a convenient registry code", async () => {
  const { businessStructureOptions } = await loadComponent(
    "src/components/onboarding/business-structure-options.ts",
  );
  const us = formsFor("US", "US-WY");
  const ambiguous = [
    ...us,
    { ...us.find((f) => f.id === "us-wy-llc"), id: "other-llc" },
  ];
  assert.equal(
    businessStructureOptions(ambiguous, "US", "US-WY", "", "").common[1].form,
    "custom",
  );
  assert.equal(
    businessStructureOptions([], "US", "US-WY", "", "").common[1].form,
    "custom",
  );
  assert.equal(
    businessStructureOptions(us, "NZ", "NZ", "", "").common[1].form,
    "custom",
    "no cross-country mapping",
  );
  const au = businessStructureOptions(formsFor("AU", "AU"), "AU", "AU", "", "");
  assert.ok(
    au.common.some((x) => x.label === "Trust" && x.form === "elf-7TPC"),
    "Australia's ordinary trust option stays available",
  );
  const caFederal = businessStructureOptions([], "CA", "ca-federal", "", "");
  assert.deepEqual(
    caFederal.common.map((x) => x.label),
    ["Corporation", "Another structure", "Not sure yet"],
  );
  const fallback = businessStructureOptions(
    formsFor("IN", "IN"),
    "IN",
    "IN",
    "",
    "",
  );
  assert.deepEqual(
    fallback.common.map((x) => x.id),
    ["custom", "unknown"],
  );
  assert.equal(fallback.reviewed, false);
  assert.equal(fallback.hasMore, true);
});

test("every scoped list keeps all registry choices reachable and preserves old specialized/custom selections", async () => {
  const { businessStructureOptions } = await loadComponent(
    "src/components/onboarding/business-structure-options.ts",
  );
  const scopes = new Map(
    reference.forms.map((f) => [
      `${f.country}/${f.jurisdiction}`,
      [f.country, f.jurisdiction],
    ]),
  );
  for (const [country, jurisdiction] of scopes.values()) {
    const forms = formsFor(country, jurisdiction);
    const result = businessStructureOptions(
      forms,
      country,
      jurisdiction,
      "",
      "",
    );
    assert.ok(
      result.common.length <= 7,
      `${country}/${jurisdiction} default stays bounded`,
    );
    for (const item of forms)
      assert.ok(result.all.some((x) => x.id === item.id));
    for (const item of result.common.filter(
      (x) => x.form !== "custom" && x.form !== "unknown",
    ))
      assert.ok(
        forms.some((f) => f.id === item.form),
        "only actual scoped IDs can be submitted",
      );
  }
  const forms = formsFor("US", "US-WY");
  const bank = businessStructureOptions(forms, "US", "US-WY", "elf-S6TP", "");
  assert.equal(bank.value, "elf-S6TP");
  assert.ok(bank.common.some((x) => x.id === "elf-S6TP"));
  const custom = businessStructureOptions(
    forms,
    "US",
    "US-WY",
    "custom",
    "Existing family activity",
  );
  assert.equal(custom.value, "custom");
  const sole = businessStructureOptions(
    forms,
    "US",
    "US-WY",
    "custom",
    "Sole proprietorship",
  );
  assert.equal(sole.value, "common:US:sole");
});

test("More structures opens the same searchable menu, selecting a specialist keeps its ID and switching back retains it", async () => {
  await withDOM(async ({ render, window }) => {
    const { BusinessStructureChoice } = await loadComponent(
      "src/components/onboarding/business-structure-choice.tsx",
    );
    const changes = [];
    function View() {
      const [value, setValue] = React.useState({
        form: "us-wy-llc",
        customForm: "",
      });
      return React.createElement(BusinessStructureChoice, {
        country: "US",
        jurisdiction: "US-WY",
        forms: formsFor("US", "US-WY"),
        ...value,
        onChange: (value) => {
          changes.push(value);
          setValue(value);
        },
      });
    }
    await render(React.createElement(View));
    const input = document.querySelector('input[role="combobox"]');
    await React.act(async () => input.click());
    assert.equal(document.querySelectorAll('[role="option"]').length, 8);
    assert.ok(
      ![...document.querySelectorAll('[role="option"]')].some(
        (x) => x.textContent === "Commercial Bank",
      ),
    );
    await React.act(async () =>
      [...document.querySelectorAll('[role="option"]')]
        .find((b) => b.textContent === "More structures")
        .click(),
    );
    await React.act(
      async () =>
        new Promise((resolve) => window.requestAnimationFrame(resolve)),
    );
    assert.equal(document.activeElement, input);
    const bank = [...document.querySelectorAll('[role="option"]')].find(
      (x) => x.textContent === "Commercial Bank",
    );
    assert.ok(bank);
    await React.act(async () => bank.click());
    assert.deepEqual(changes, [{ form: "elf-S6TP", customForm: "" }]);
    await React.act(async () =>
      [...document.querySelectorAll("button")]
        .find((b) => b.textContent === "Common structures")
        .click(),
    );
    await React.act(
      async () =>
        new Promise((resolve) => window.requestAnimationFrame(resolve)),
    );
    assert.equal(
      document.querySelector('select[name="legal_form"]').value,
      "elf-S6TP",
    );
    assert.deepEqual(changes, [{ form: "elf-S6TP", customForm: "" }]);
    await React.act(async () =>
      [...document.querySelectorAll('[role="option"]')]
        .find((x) => x.textContent === "Sole proprietorship")
        .click(),
    );
    assert.deepEqual(changes.at(-1), {
      form: "custom",
      customForm: "Sole proprietorship",
    });
    assert.equal(
      document.querySelector('[name="custom_legal_form"]'),
      null,
      "a chosen preset needs no repeated typing",
    );
  });
});

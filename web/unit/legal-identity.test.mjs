import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = { id: "business-identity-book", profile: "business" };
const catalog = {
  version: "fixture-1",
  countries: [{ code: "US", name: "United States" }],
  jurisdictions: [{ id: "US-WY", country_code: "US", name: "Wyoming" }],
  forms: [
    {
      id: "us-wy-llc",
      country_code: "US",
      jurisdiction_ids: ["US-WY"],
      local_name: "Limited liability company",
      aliases: ["LLC"],
    },
  ],
  overlays: [],
  tax_classifications: [
    { id: "us-s", country_code: "US", name: "S corporation" },
  ],
};
const response = (data) => ({ ok: true, json: async () => data });
const control = (label) => {
  const node = [...document.querySelectorAll("label")].find(
    (l) => l.textContent.replaceAll("*", "").trim() === label,
  );
  assert.ok(node, label);
  const presented = document.getElementById(node.htmlFor);
  return presented?.tagName === "BUTTON" ||
    presented?.getAttribute("role") === "combobox"
    ? node.closest(".field").querySelector("select")
    : presented;
};
async function set(window, label, value) {
  await React.act(async () => {
    const node = control(label);
    if (node.tagName === "SELECT") {
      node.value = value;
      node.dispatchEvent(new window.Event("change", { bubbles: true }));
    } else {
      Object.getOwnPropertyDescriptor(
        window.HTMLInputElement.prototype,
        "value",
      ).set.call(node, value);
      node.dispatchEvent(new window.Event("input", { bubbles: true }));
    }
  });
}
const submit = (window) =>
  React.act(async () =>
    document
      .querySelector("form")
      .dispatchEvent(
        new window.Event("submit", { bubbles: true, cancelable: true }),
      ),
  );

test("formation, registration and tax facts stay separate through an uncertain identity save and remount", async () => {
  await withDOM(async ({ render, window }) => {
    const { LegalIdentitySettings } = await loadComponent(
      "src/components/book/legal-identity-settings.tsx",
    );
    const oldFetch = globalThis.fetch;
    const oldStorage = Object.getOwnPropertyDescriptor(
      globalThis,
      "sessionStorage",
    );
    Object.defineProperty(globalThis, "sessionStorage", {
      configurable: true,
      value: window.sessionStorage,
    });
    const requests = [];
    let uncertain = true,
      saved = 0;
    globalThis.fetch = async (url, init) => {
      if (url.endsWith("/session")) return response({ csrf: "fixture" });
      if (url.endsWith("/legal-identity-catalog")) return response({ catalog });
      if (!init?.body)
        return response({
          subject: {
            id: "subject",
            revision: 0,
            kind: null,
            identity: { registration_status: "not_registered" },
          },
          history: [],
          has_more: false,
        });
      requests.push(init.body);
      if (uncertain) throw new Error("Identity response lost");
      return response({ applied_revision: 1 });
    };
    const props = {
      book,
      onSaved() {
        saved++;
      },
    };
    try {
      await render(React.createElement(LegalIdentitySettings, props));
      await set(window, "Subject kind", "legal_entity");
      await set(window, "Formation or governing country", "US");
      await set(window, "Legal jurisdiction", "US-WY");
      await set(window, "Legal form", "us-wy-llc");
      await set(window, "Separate tax classification", "us-s");
      await set(
        window,
        "Registration authority (optional)",
        "Synthetic authority",
      );
      await set(window, "Registration number (optional)", "fixture-123");
      await set(window, "Registration country code (optional)", "CA");
      await set(window, "Reason for identity change", "Owner supplied facts");
      await submit(window);
      assert.match(document.body.textContent, /may already be saved/);
      assert.doesNotMatch(document.body.textContent, /Identity response lost/);
      const sent = JSON.parse(requests[0]);
      assert.equal(sent.identity.registration_status, "not_registered");
      assert.equal(sent.identity.formation_or_governing_country_code, "US");
      assert.equal(sent.identity.registrations[0].country_code, "CA");
      assert.equal(sent.identity.legal_form_id, "us-wy-llc");
      assert.deepEqual(sent.identity.tax_classification_ids, ["us-s"]);
      assert.equal(sent.effective_date, "");
      assert.equal(sent.revision, 0);
      assert.equal(sent.kind, "legal_entity");
      await render(null);
      await render(React.createElement(LegalIdentitySettings, props));
      assert.equal(control("Formation or governing country").value, "US");
      assert.ok(control("Reason for identity change").matches(":disabled"));
      uncertain = false;
      await submit(window);
      assert.equal(requests[1], requests[0]);
      assert.equal(saved, 1);
    } finally {
      globalThis.fetch = oldFetch;
      if (oldStorage)
        Object.defineProperty(globalThis, "sessionStorage", oldStorage);
      else delete globalThis.sessionStorage;
    }
  });
});

test("Settings identity is summary-first and name edits preserve secondary facts through review and exact retry", async () => {
  await withDOM(async ({ render, window }) => {
    const { LegalIdentitySettings } = await loadComponent(
      "src/components/book/legal-identity-settings.tsx",
    );
    const previousFetch = globalThis.fetch;
    const previousStorage = Object.getOwnPropertyDescriptor(
      globalThis,
      "sessionStorage",
    );
    Object.defineProperty(globalThis, "sessionStorage", {
      configurable: true,
      value: window.sessionStorage,
    });
    const facts = {
      legal_name: "Original LLC",
      registration_status: "registered",
      formation_or_governing_country_code: "US",
      legal_jurisdiction_id: "US-WY",
      legal_form_id: "us-wy-llc",
      overlay_ids: ["designation-a", "designation-b"],
      custom_overlays: ["Recorded designation"],
      tax_classification_ids: ["us-s", "second-tax-fact"],
      custom_tax_classifications: ["Recorded tax fact"],
      tax_residence_country_codes: ["US", "CA"],
      registrations: [
        {
          authority: "Registry",
          number: "123",
          country_code: "CA",
          label: "Original label",
        },
        { authority: "Second registry", number: "456", country_code: "US" },
      ],
    };
    const requests = [];
    let uncertain = true;
    globalThis.fetch = async (url, init) => {
      if (url.endsWith("/session")) return response({ csrf: "fixture" });
      if (url.endsWith("/legal-identity-catalog")) return response({ catalog });
      if (init?.method === "POST") {
        requests.push(init.body);
        if (uncertain) throw Error("Response lost");
        return response({ applied_revision: 2 });
      }
      return response({
        subject: {
          id: "subject",
          kind: "legal_entity",
          revision: 1,
          identity: facts,
        },
        history: [],
        has_more: false,
      });
    };
    const props = { book, presentation: "settings", onSaved() {} };
    try {
      await render(React.createElement(LegalIdentitySettings, props));
      assert.equal(document.querySelector("form"), null);
      assert.match(document.body.textContent, /S corporation/);
      await React.act(async () =>
        [...document.querySelectorAll("button")]
          .find((b) => b.textContent === "Edit details")
          .click(),
      );
      assert.equal(
        document.querySelector('[name="reason"]').matches(":disabled"),
        true,
      );
      assert.equal(document.querySelectorAll("details[open]").length, 0);
      await set(window, "Legal name", "Updated LLC");
      await submit(window);
      assert.equal(requests.length, 0, "Review is not a write");
      assert.match(
        document.querySelector('[aria-label="Review identity changes"]')
          .textContent,
        /Original LLC.*Updated LLC/,
      );
      await set(window, "Reason for change", "Correct the legal name");
      await submit(window);
      const sent = JSON.parse(requests[0]);
      for (const key of [
        "overlay_ids",
        "custom_overlays",
        "tax_classification_ids",
        "custom_tax_classifications",
        "tax_residence_country_codes",
        "registrations",
      ])
        assert.deepEqual(
          sent.identity[key],
          facts[key],
          key + " survives closed advanced fields",
        );
      assert.equal(sent.identity.legal_name, "Updated LLC");
      assert.equal(sent.effective_date, "");
      assert.equal(sent.revision, 1);
      await render(null);
      await render(React.createElement(LegalIdentitySettings, props));
      assert.ok(
        document
          .querySelector('button[type="submit"]')
          .textContent.includes("Retry identity change"),
      );
      uncertain = false;
      await submit(window);
      assert.equal(requests[1], requests[0]);
    } finally {
      globalThis.fetch = previousFetch;
      if (previousStorage)
        Object.defineProperty(globalThis, "sessionStorage", previousStorage);
      else delete globalThis.sessionStorage;
    }
  });
});

test("Settings uses the common structure flow without submitting UI option keys or inferred subject kinds", async () => {
  await withDOM(async ({ render, window }) => {
    const { LegalIdentitySettings } = await loadComponent(
      "src/components/book/legal-identity-settings.tsx",
    );
    const previous = globalThis.fetch;
    let sent;
    globalThis.fetch = async (url, init) => {
      if (url.endsWith("/session")) return response({ csrf: "fixture" });
      if (url.endsWith("/legal-identity-catalog")) return response({ catalog });
      if (init?.method === "POST") {
        sent = JSON.parse(init.body);
        return response({ applied_revision: 1 });
      }
      return response({
        subject: { id: "subject", kind: null, revision: 0, identity: {} },
        history: [],
        has_more: false,
      });
    };
    try {
      await render(
        React.createElement(LegalIdentitySettings, {
          book,
          presentation: "settings",
          onSaved() {},
        }),
      );
      assert.equal(document.querySelector("form"), null);
      await React.act(async () =>
        [...document.querySelectorAll("button")]
          .find((b) => b.textContent === "Add details")
          .click(),
      );
      await set(window, "Country", "US");
      await set(window, "State of formation", "US-WY");
      const structure = control("Business structure");
      const sole = [...structure.options].find(
        (o) => o.textContent === "Sole proprietorship",
      );
      assert.ok(sole);
      await set(window, "Business structure", sole.value);
      await submit(window);
      assert.equal(sent, undefined);
      await set(window, "Reason for change", "Record the business structure");
      await submit(window);
      assert.equal(Object.hasOwn(sent, "kind"), false);
      assert.equal(sent.identity.legal_form_id, "");
      assert.equal(sent.identity.custom_legal_form, "Sole proprietorship");
      assert.equal(sent.identity.formation_or_governing_country_code, "US");
      assert.equal(JSON.stringify(sent).includes("common:"), false);
    } finally {
      globalThis.fetch = previous;
    }
  });
});

test("identity history exposes the old and new classification without inventing an effective date", async () => {
  await withDOM(async ({ render }) => {
    const { LegalIdentitySettings } = await loadComponent(
      "src/components/book/legal-identity-settings.tsx",
    );
    const oldFetch = globalThis.fetch;
    const identity = {
      catalog_version: "fixture-1",
      country: catalog.countries[0],
      jurisdiction: catalog.jurisdictions[0],
      legal_form: catalog.forms[0],
      tax_classifications: catalog.tax_classifications,
    };
    globalThis.fetch = async (url) =>
      response(
        url.endsWith("/legal-identity-catalog")
          ? { catalog }
          : {
              subject: {
                id: "subject",
                revision: 1,
                kind: "legal_entity",
                identity,
              },
              history: [
                {
                  id: "history",
                  prior_kind: null,
                  new_kind: "legal_entity",
                  prior_identity: {},
                  new_identity: identity,
                  actor: "owner",
                  reason: "Owner supplied facts",
                  effective_date: null,
                  recorded_at: "2026-10-25T12:00:00Z",
                },
              ],
              has_more: false,
            },
      );
    try {
      await render(
        React.createElement(LegalIdentitySettings, { book, onSaved() {} }),
      );
      const history = document.querySelector(".identity-history");
      assert.match(history.textContent, /View prior and new facts/);
      const sides = history.querySelectorAll("details dl");
      assert.equal(sides.length, 2);
      assert.match(sides[0].textContent, /Subject kindUnknown/);
      assert.match(sides[1].textContent, /Subject kindlegal entity/);
      assert.match(sides[1].textContent, /Wyoming/);
      assert.match(sides[1].textContent, /Tax classificationS corporation/);
      assert.match(
        history.textContent,
        /Effective date: Unknown \/ not supplied/,
      );
    } finally {
      globalThis.fetch = oldFetch;
    }
  });
});

test("inline identity settings show the confirmed revision and history after saving without closing the page", async () => {
  await withDOM(async ({ render, window }) => {
    const { LegalIdentitySettings } = await loadComponent(
      "src/components/book/legal-identity-settings.tsx",
    );
    const previous = globalThis.fetch;
    let revision = 0,
      identityReads = 0;
    globalThis.fetch = async (url, init) => {
      if (url.endsWith("/session"))
        return response({ csrf: "synthetic-csrf" });
      if (url.endsWith("/legal-identity-catalog")) return response({ catalog });
      if (init?.method === "POST") {
        revision = 1;
        return response({ applied_revision: 1 });
      }
      identityReads++;
      return response({
        subject: {
          id: "synthetic-subject",
          revision,
          kind: null,
          identity: {},
        },
        history: revision
          ? [
              {
                id: "synthetic-change",
                prior_kind: null,
                new_kind: null,
                prior_identity: {},
                new_identity: {},
                reason: "Synthetic inline revision",
                recorded_at: "2026-10-26T12:00:00Z",
                actor: "Synthetic actor",
                effective_date: "",
              },
            ]
          : [],
        has_more: false,
      });
    };
    function Inline() {
      const [refreshKey, setRefreshKey] = React.useState(0);
      return React.createElement(LegalIdentitySettings, {
        book,
        presentation: "settings",
        refreshKey,
        onSaved() {
          setRefreshKey((key) => key + 1);
        },
      });
    }
    try {
      await render(React.createElement(Inline));
      const section = document.querySelector(".vult-settings-section");
      await React.act(async () =>
        [...document.querySelectorAll("button")]
          .find((b) => b.textContent === "Add details")
          .click(),
      );
      await set(window, "Legal name", "Synthetic Entity");
      await submit(window);
      await set(window, "Reason for change", "Synthetic inline revision");
      await submit(window);
      assert.ok(document.querySelector(".vult-settings-section") === section);
      assert.equal(identityReads, 2);
      assert.match(
        document.querySelector(".identity-history").textContent,
        /Synthetic inline revision/,
      );
      assert.equal(document.querySelector("form"), null);
      assert.ok(
        [...document.querySelectorAll("button")].some(
          (button) => button.textContent.trim() === "Add details",
        ),
      );
      assert.equal(document.querySelector('[role="dialog"]'), null);
    } finally {
      globalThis.fetch = previous;
    }
  });
});

import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

test("human notes never fall back to legacy notes or import traces", async () => {
  const { EntryUserNote } = await loadComponent(
    "src/components/book/entry-user-note.tsx",
  );
  await withDOM(async ({ render }) => {
    const audit = {
      note: "Legacy parser trace",
      import_provenance: { audit_text: "Import diagnostics" },
    };
    await render(React.createElement(EntryUserNote, { entry: audit }));
    assert.equal(document.body.textContent, "");
    await render(
      React.createElement(EntryUserNote, {
        entry: { ...audit, user_note: "Owner comment" },
      }),
    );
    assert.match(document.body.textContent, /Owner comment/);
    assert.doesNotMatch(document.body.textContent, /parser|diagnostics/);
  });
});

test("provenance remains readable text and never fabricates a file link", async () => {
  const { ImportProvenanceDetails } = await loadComponent(
    "src/components/book/import-provenance.tsx",
  );
  await withDOM(async ({ render }) => {
    await render(
      React.createElement(ImportProvenanceDetails, {
        value: {
          version: 1,
          provider: "example",
          source_reference: "https://example.invalid/source.csv",
          audit_text: "Preserved parser audit",
        },
      }),
    );
    assert.match(document.body.textContent, /Preserved parser audit/);
    assert.equal(document.querySelector("a"), null);
    assert.equal(document.querySelector("img"), null);
    assert.equal(document.querySelector("dl.meta > div"), null);
  });
});

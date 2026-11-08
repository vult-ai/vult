import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";

test("a Book's logo is read only from a valid reference, and its address changes with its digest", async () => {
  const { bookLogo, bookLogoURL } = await loadComponent("src/lib/book-logo.ts");
  const logo = { id: "1b2c3d4e-0000-4000-8000-000000000001", sha256: "a".repeat(64), content_type: "image/jpeg", size: 9000 };
  const book = { id: "book-1", name: "Example Co.", logo };
  assert.deepEqual(bookLogo(book), logo);
  assert.equal(bookLogoURL(book), `/api/v1/book-identity/logo/${logo.id}?book_id=book-1&revision=${"a".repeat(64)}`);
  assert.equal(bookLogo({ logo: { ...logo, content_type: "image/svg+xml" } }), undefined, "never an SVG");
  assert.equal(bookLogo({ logo: { ...logo, size: 500000 } }), undefined, "never more than the stored size");
  assert.equal(bookLogo({ logo: null }), undefined);
  assert.equal(bookLogoURL({ id: "book-1" }), undefined);
});

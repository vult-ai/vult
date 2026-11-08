import assert from "node:assert/strict";
import test from "node:test";
import { execFileSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const { checkSource } = await import("../scripts/check-product-terms.mjs");

test("Book is a proper noun in interface copy, not in code or accounting terms", () => {
  for (const copy of [
    "<p>Create book</p>",
    'label="Opening your books…"',
    "Choose one book for this view.",
    "stored in this book’s database",
  ])
    assert.equal(checkSource("components/page.tsx", copy).length, 1, copy);
  for (const allowed of [
    "const book = state.book;",
    "books.map((book) => book.id)",
    "Estimated book value",
    "Open a Book you can access.",
    "// the selected book",
  ])
    assert.equal(
      checkSource("components/page.tsx", allowed).length,
      0,
      allowed,
    );
});

test("the application source follows the Book convention", () => {
  execFileSync(
    process.execPath,
    [
      fileURLToPath(
        new URL("../scripts/check-product-terms.mjs", import.meta.url),
      ),
    ],
    { stdio: "pipe" },
  );
});

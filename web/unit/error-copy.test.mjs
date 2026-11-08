import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { loadComponent, React, withDOM } from "./helpers/dom.mjs";
const policy = await loadComponent("src/lib/errors.ts");
const golden = JSON.parse(
  await readFile(
    new URL("./fixtures/error-copy.json", import.meta.url),
    "utf8",
  ),
);

test("reviewed copy fixtures preserve the specific reason, outcome and safe recovery", () => {
  for (const example of golden) {
    const error = example.local
      ? new Error(example.local)
      : policy.responseFailure(
          example.status,
          { error: example.raw ?? "unreviewed server prose" },
          example.intent,
        );
    const result = policy.presentFailure(error, {
      intent: example.intent,
      ...example.context,
    });
    assert.equal(result.message, example.message, example.id);
    if (example.recovery)
      assert.equal(result.recovery, example.recovery, example.id);
    if (example.outcome)
      assert.equal(result.outcome, example.outcome, example.id);
    assert.ok(
      result.message.split(/\s+/).length <= 18,
      example.id + " is too wordy",
    );
  }
});

test("copy never guesses a cause, a file limit, a deletion or a duplicate posting", () => {
  const access = policy.responseFailure(403, {}).message;
  assert.doesNotMatch(access, /Book owner|administrator|deleted/i);
  assert.doesNotMatch(
    policy.responseFailure(413, {}, "write").message,
    /10|48|MB/,
  );
  assert.doesNotMatch(
    policy.responseFailure(404, {}).message,
    /deleted|removed|never existed/,
  );
  const conflict = policy.responseFailure(
    409,
    { error: "duplicate maybe financial payload secret" },
    "write",
  );
  assert.equal(conflict.failure.outcome, "unknown");
  assert.doesNotMatch(
    conflict.message,
    /duplicate|not saved|failed to save|recorded twice/i,
  );
  const unknown = policy.responseFailure(
    400,
    { error: "new unsupported private reason" },
    "write",
  );
  assert.equal(unknown.message, "Check your details and try again.");
});

test("gallery cases use the real feedback components, include field associations and no diagnostics", async () => {
  await withDOM(async ({ render }) => {
    const { ErrorCatalog } = await loadComponent(
      "tests/fixtures/error-catalog.tsx",
    );
    await render(React.createElement(ErrorCatalog));
    assert.equal(document.querySelectorAll(".copy-example").length, 25);
    assert.ok(document.querySelector('input[aria-invalid="true"]'));
    assert.equal(
      document.querySelectorAll(".error-notice details,.error-notice summary")
        .length,
      0,
    );
    assert.doesNotMatch(
      document.querySelector(".copy-catalog").textContent,
      /HTTP|SQLSTATE|endpoint|Technical details/,
    );
  });
});

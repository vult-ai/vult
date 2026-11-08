import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
const policy = await loadComponent("tests/fixtures/error-policy.ts");
const book = { id: "preview", currency: "USD", scale: 2 };
const header =
  "book_id,currency,transaction_id,line_id,account_code,side,amount_minor\n";
const unbalanced = header + "preview,USD,entry,line1,cash,debit,100\n";
const mismatch = () => {
  try {
    policy.parseTransactionJournal(unbalanced, book);
  } catch (error) {
    return error;
  }
  assert.fail("The real validator must reject an unbalanced read.");
};

test("transport failures, copied messages and server urgency flags cannot manufacture critical authority", () => {
  for (const status of [0, 400, 401, 403, 409, 422, 429, 500, 503, 507]) {
    for (const intent of ["read", "write"]) {
      const error = policy.responseFailure(
        status,
        {
          error: "The journal read contains an unbalanced entry.",
          code: "LEDGER_CORRUPT",
          severity: "critical",
          verified: true,
        },
        intent,
      );
      assert.notEqual(
        policy.presentFailure(error, { intent }).severity,
        "critical",
      );
    }
  }
  assert.notEqual(
    policy.presentFailure(new Error(mismatch().message)).severity,
    "critical",
  );
  assert.equal(
    policy.presentFailure(policy.responseFailure(504, {}, "write")).severity,
    "attention",
  );
  assert.equal(
    policy.presentFailure(policy.responseFailure(503, {}), { intent: "read" })
      .severity,
    "routine",
  );
});

test("real ledger and source checks grant critical treatment without claiming writes are paused", async () => {
  const error = mismatch();
  assert.match(error.message, /unbalanced entry/);
  const result = policy.presentFailure(error);
  assert.equal(result.severity, "critical");
  assert.equal(result.outcome, "read");
  assert.equal(result.message, "We couldn’t verify these records.");
  assert.doesNotMatch(result.message, /paused|lost|corrupt|saved/i);
  assert.equal(policy.isConfirmedRejection(error), false);
  assert.equal(
    policy.mostUrgentFailure(policy.responseFailure(503, {}), error),
    error,
  );
});

test("a verified critical review retains its surface and draft, and uses the primary read-only action", async () => {
  await withDOM(async ({ render }) => {
    let finish,
      reviews = 0,
      posts = 0;
    await render(
      React.createElement(
        "section",
        null,
        React.createElement("input", { defaultValue: "Keep my draft" }),
        React.createElement(policy.ErrorNotice, {
          error: mismatch(),
          intent: "read",
          onReview: async () => {
            reviews++;
            await new Promise((resolve) => (finish = resolve));
          },
          onRetry: () => posts++,
        }),
      ),
    );
    const frame = document.querySelector(".error-notice"),
      input = document.querySelector("input"),
      button = frame.querySelector("button");
    assert.equal(frame.dataset.severity, "critical");
    assert.equal(
      frame.querySelector('[role="alert"]').textContent,
      "We couldn’t verify these records.",
    );
    assert.match(button.className, /primary/);
    assert.equal(button.textContent, "Review");
    await React.act(async () => button.click());
    assert.equal(reviews, 1);
    assert.equal(posts, 0);
    assert.equal(document.querySelector(".error-notice"), frame);
    assert.equal(input.value, "Keep my draft");
    await React.act(async () => finish());
    assert.equal(
      document.querySelector(".error-notice"),
      frame,
      "finishing a review does not establish successful recovery",
    );
  });
});

test("a failed review never turns an uncertain save into a rejected or retryable write", async () => {
  await withDOM(async ({ render }) => {
    const error = policy.responseFailure(504, {}, "write");
    await render(
      React.createElement(
        "section",
        null,
        React.createElement("input", {
          defaultValue: "Frozen date and receipt",
        }),
        React.createElement(policy.ErrorNotice, {
          error,
          intent: "write",
          onRetry: () => assert.fail("no mutation retry"),
          onReview: async () => {
            throw new TypeError("Failed to fetch");
          },
        }),
      ),
    );
    await React.act(async () => document.querySelector("button").click());
    assert.equal(
      document.querySelector(".error-notice").dataset.severity,
      "attention",
    );
    assert.match(
      document.querySelector(".error-notice").textContent,
      /couldn’t check/,
    );
    assert.equal(
      document.querySelector("input").value,
      "Frozen date and receipt",
    );
    assert.equal(policy.isConfirmedRejection(error), false);
    assert.equal(document.querySelector("button").textContent, "Review");
  });
});

test("journal hook keeps verified identity through a failed read while preserving its string error API", async () => {
  const original = globalThis.fetch;
  try {
    globalThis.fetch = async () =>
      Response.json({version:1,book_id:book.id,currency:book.currency,scale:book.scale,content_sha256:"a".repeat(64),entries:[{id:"entry",lines:[{id:"line1",account_code:"cash",side:"debit",amount_minor:"100"},{id:"line2",account_code:"equity",side:"credit",amount_minor:"99"}]}]});
    await withDOM(async ({ render }) => {
      let value;
      function Harness() {
        value = policy.useTransactionJournal(book, 1, true, ["entry"]);
        return React.createElement(policy.ErrorNotice, {
          error: value.failure || value.error,
          intent: "read",
          onRetry: value.retry,
        });
      }
      await render(React.createElement(Harness));
      await React.act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 20));
      });
      assert.equal(typeof value.error, "string");
      assert.equal(policy.presentFailure(value.failure).severity, "critical");
      assert.equal(
        document.querySelector(".error-notice").dataset.severity,
        "critical",
      );
    });
  } finally {
    globalThis.fetch = original;
  }
});

test("contributor guard rejects page-defined urgency and unapproved critical factories", async () => {
  const { checkSource } = await import("../scripts/check-error-handling.mjs");
  assert.ok(
    checkSource("components/page.tsx", '<ErrorNotice severity="critical" />')
      .length,
  );
  assert.ok(
    checkSource(
      "components/page.tsx",
      'verifiedReadFailure("ledger-balance", "guess")',
    ).length,
  );
  assert.equal(
    checkSource(
      "lib/transaction-journal.ts",
      'verifiedReadFailure("ledger-balance", "verified")',
    ).length,
    0,
  );
});

test("verified review failures can become critical without changing the unknown posting outcome", async () => {
  await withDOM(async ({ render }) => {
    const original = policy.responseFailure(504, {}, "write");
    await render(
      React.createElement(policy.ErrorNotice, {
        error: original,
        intent: "write",
        onRetry: () =>
          assert.fail("a critical review does not authorize another posting"),
        onReview: async () => {
          throw mismatch();
        },
      }),
    );
    await React.act(async () => document.querySelector("button").click());
    assert.equal(
      document.querySelector(".error-notice").dataset.severity,
      "critical",
    );
    assert.match(
      document.querySelector(".error-notice").textContent,
      /couldn’t verify/,
    );
    assert.equal(original.failure.outcome, "unknown");
    assert.equal(policy.isConfirmedRejection(original), false);
    assert.equal(document.querySelector("button").textContent, "Review");
  });
});

test("identical journal delivery duplicates still skip safely; conflicting values remain rejected", () => {
  const credit = "preview,USD,entry,line2,equity,credit,100\n";
  const debit = "preview,USD,entry,line1,cash,debit,100\n";
  assert.equal(
    policy
      .parseTransactionJournal(unbalanced + credit + debit, book)
      .get("entry").length,
    2,
  );
  assert.throws(
    () =>
      policy.parseTransactionJournal(
        unbalanced + credit + debit.replace(",100", ",200"),
        book,
      ),
    (e) => policy.presentFailure(e).severity === "critical",
  );
  assert.throws(
    () =>
      policy.currentPosition({
        account_balances: [
          { code: "cash", kind: "asset", balance_minor: "100" },
        ],
      }),
    (e) => policy.presentFailure(e).severity === "critical",
  );
});

test("an old Book's verified failure cannot appear in another Book or a disabled journal view", async () => {
  const original = globalThis.fetch;
  try {
    globalThis.fetch = async () =>
      Response.json({version:1,book_id:book.id,currency:book.currency,scale:book.scale,content_sha256:"a".repeat(64),entries:[{id:"entry",lines:[{id:"line1",account_code:"cash",side:"debit",amount_minor:"100"},{id:"line2",account_code:"equity",side:"credit",amount_minor:"99"}]}]});
    await withDOM(async ({ render }) => {
      let value;
      function Harness({ selected, enabled }) {
        value = policy.useTransactionJournal(selected, 1, enabled, ["entry"]);
        return React.createElement(policy.ErrorNotice, {
          error: value.failure || value.error,
          intent: "read",
        });
      }
      await render(
        React.createElement(Harness, { selected: book, enabled: true }),
      );
      await React.act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 20));
      });
      assert.equal(
        document.querySelector(".error-notice").dataset.severity,
        "critical",
      );
      await render(
        React.createElement(Harness, {
          selected: { ...book, id: "another-book" },
          enabled: false,
        }),
      );
      assert.equal(value.failure, undefined);
      assert.equal(value.error, "");
      assert.equal(document.querySelector(".error-notice"), null);
    });
  } finally {
    globalThis.fetch = original;
  }
});

test("native startup feedback uses canonical severity, a quiet outlined icon and safe copy without the React chunk", async () => {
  await withDOM(async () => {
    const target = document.createElement("div");
    document.body.append(target);
    policy.showStartupError(
      target,
      new TypeError(
        "Unable to preload CSS for http://127.0.0.1:51620/assets/private-onboarding.css",
      ),
    );
    assert.equal(target.firstElementChild.dataset.severity, "routine");
    assert.ok(target.querySelector("svg circle"));
    assert.equal(target.firstElementChild.tagName, "MAIN");
    assert.ok(target.querySelector("h1"));
    assert.ok(target.querySelector(".startup-error-screen"));
    assert.equal(document.activeElement, target.querySelector("h1"));
    assert.doesNotMatch(target.textContent, /private|endpoint|HTTP|SQL|CSS|preload|diagnostic|51620/);
    policy.showStartupError(target, mismatch());
    assert.equal(target.firstElementChild.dataset.severity, "critical");
    assert.ok(target.querySelector("svg polygon"));
    assert.match(target.querySelector("button").className, /primary/);
    assert.match(target.textContent, /couldn’t verify these records/);
    assert.doesNotMatch(target.textContent, /paused|lost|corrupt/);
  });
});

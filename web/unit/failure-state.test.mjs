import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
// One module graph keeps verified error identity intact.
const policy = await loadComponent("tests/fixtures/error-policy.ts");
const { FailureState } = policy;

const integrity = () => {
  try {
    policy.parseTransactionJournal(
      "book_id,currency,transaction_id,line_id,account_code,side,amount_minor\npreview,USD,entry,line1,cash,debit,100\n",
      { id: "preview", currency: "USD", scale: 2 },
    );
  } catch (error) {
    return error;
  }
};
const buttons = () =>
  [...document.querySelectorAll(".failure-state button, .failure-state a")].map(
    (b) => b.textContent,
  );

test("a page failure names what failed, gives one supporting line and one action", async () => {
  await withDOM(async ({ render }) => {
    await render(
      React.createElement(FailureState, {
        scope: "page",
        error: policy.responseFailure(503, {
          error: "endpoint SQLSTATE /private",
          code: "INTERNAL_REFERENCE",
        }),
        title: "Couldn’t open Example Studio",
        onRetry: () => {},
      }),
    );
    const state = document.querySelector(".failure-state");
    assert.equal(state.dataset.scope, "page");
    assert.equal(state.dataset.severity, "routine");
    assert.equal(state.querySelector("h1").textContent, "Couldn’t open Example Studio");
    assert.equal(state.querySelectorAll("p").length, 1);
    assert.equal(state.querySelector("p").textContent, "This is usually temporary.");
    assert.deepEqual(buttons(), ["Try again"]);
    assert.doesNotMatch(state.textContent, /503|SQLSTATE|INTERNAL_REFERENCE|private/);
    assert.equal(document.activeElement, state.querySelector("h1"));
  });
});

test("recovery picks the action: none, sign-in, and no retry for writes", async () => {
  await withDOM(async ({ render }) => {
    await render(
      React.createElement(FailureState, {
        scope: "region",
        error: policy.responseFailure(404, {}),
        resource: "document",
        onRetry: () => {},
      }),
    );
    assert.equal(document.querySelector("h2").textContent, "Couldn’t load this document");
    assert.deepEqual(buttons(), []);
    await render(
      React.createElement(FailureState, {
        scope: "region",
        error: policy.responseFailure(401, {}),
        resource: "accounts",
      }),
    );
    assert.deepEqual(buttons(), ["Sign in"]);
    // Signed out is a state, not a failure to load.
    assert.match(document.body.textContent, /You’re signed out/);
    assert.doesNotMatch(document.body.textContent, /Couldn’t/);
    assert.equal(document.querySelector("p").textContent, "Sign in to open your Books.");
    await render(
      React.createElement(FailureState, {
        scope: "region",
        error: policy.responseFailure(503, {}, "write"),
        intent: "write",
        onRetry: () => {},
      }),
    );
    assert.deepEqual(buttons(), []);
  });
});

test("only verified integrity failures are critical, and they lead to review", async () => {
  await withDOM(async ({ render }) => {
    await render(
      React.createElement(FailureState, {
        scope: "page",
        error: integrity(),
        resource: "statement",
        onRetry: () => {},
        onReview: () => {},
      }),
    );
    const state = document.querySelector(".failure-state");
    assert.equal(state.dataset.severity, "critical");
    assert.ok(state.querySelector('[role="alert"]'));
    assert.deepEqual(buttons(), ["Review"]);
    assert.match(state.textContent, /couldn’t verify these records/);
    assert.doesNotMatch(state.textContent, /paused|lost|corrupt|saved/i);
    for (const status of [0, 401, 403, 409, 429, 500, 503]) {
      await render(
        React.createElement(FailureState, {
          scope: "region",
          error: policy.responseFailure(status, { severity: "critical" }),
        }),
      );
      assert.notEqual(
        document.querySelector(".failure-state").dataset.severity,
        "critical",
        String(status),
      );
    }
  });
});

test("the states gallery renders every read failure at every scope through the real components", async () => {
  await withDOM(async () => {
    document.body.innerHTML = '<div id="root"></div>';
    await loadComponent("tests/fixtures/states.tsx");
    await new Promise((resolve) => setTimeout(resolve, 50));
    const cases = document.querySelectorAll(".gallery-case");
    assert.ok(cases.length >= 12);
    assert.equal(
      document.querySelectorAll('.failure-state[data-scope="page"]').length,
      8,
    );
    assert.equal(
      document.querySelectorAll('.failure-state[data-scope="region"]').length,
      8,
    );
    assert.ok(document.querySelector('.failure-state[data-severity="critical"]'));
    assert.doesNotMatch(
      document.querySelector(".gallery").textContent,
      /SQLSTATE|endpoint|Technical details/,
    );
    assert.ok(document.querySelector('.loading-state[role], .loading-state [role="status"]'));
    assert.ok(document.querySelector(".empty-state"));
    // The lifecycle demo settles on its failure after one timed read.
    await new Promise((resolve) => setTimeout(resolve, 1400));
    assert.equal(
      document.querySelectorAll('.failure-state[data-scope="page"]').length,
      9,
    );
  });
});

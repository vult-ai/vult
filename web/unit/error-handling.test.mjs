import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";
const {
  APIError,
  fetchJSON,
  responseFailure,
  isConfirmedRejection,
  errorMessage,
} = await loadComponent("src/lib/errors.ts");

test("server prose and malformed responses never become UI copy; aborts stay cancellations", async () => {
  for (const status of [400, 401, 403, 404, 409, 422, 429, 500, 503]) {
    const error = responseFailure(status, {
      error: "endpoint not found SQLSTATE /Users/private?secret=abc",
      code: "READ_UNAVAILABLE",
    });
    assert.doesNotMatch(error.message, /endpoint|SQLSTATE|Users|secret/);
    assert.equal(error.status, status);
    assert.equal(error.failure.code, "READ_UNAVAILABLE");
  }
  assert.match(
    responseFailure(422, { error: "debits and credits must balance" }, "write")
      .message,
    /both sides need to match/,
  );
  assert.match(
    errorMessage(new Error("Account activity returned an unexpected response")),
    /couldn’t finish/,
  );
  const original = globalThis.fetch;
  try {
    globalThis.fetch = async () => new Response("<html>private</html>");
    await assert.rejects(
      fetchJSON("/read"),
      (e) => e instanceof APIError && !e.message.includes("JSON"),
    );
    globalThis.fetch = async () => {
      throw new TypeError("Failed to fetch private-host");
    };
    await assert.rejects(fetchJSON("/read"), /couldn’t connect/);
    const abort = new DOMException("Cancelled", "AbortError");
    globalThis.fetch = async () => {
      throw abort;
    };
    await assert.rejects(fetchJSON("/read"), (e) => e === abort);
  } finally {
    globalThis.fetch = original;
  }
});

test("uncertain writes keep their identity; transport never automatically retries a POST", async () => {
  for (const status of [0, 408, 409, 429, 500, 503])
    assert.equal(
      isConfirmedRejection(responseFailure(status, {}, "write")),
      false,
      String(status),
    );
  assert.equal(
    isConfirmedRejection(responseFailure(200, {}, "write", true)),
    false,
  );
  for (const status of [400, 401, 403, 404, 413, 415, 422])
    assert.equal(
      isConfirmedRejection(responseFailure(status, {}, "write")),
      true,
      String(status),
    );
  const original = globalThis.fetch,
    calls = [];
  const init = {
    method: "POST",
    body: JSON.stringify({
      request_key: "same-key",
      date: "2026-10-25",
      amount: "1234",
    }),
  };
  try {
    globalThis.fetch = async (url, options) => {
      calls.push({ url, options });
      throw new TypeError("Response lost after commit");
    };
    await assert.rejects(
      fetchJSON("/save", init),
      (e) => e.failure.outcome === "unknown",
    );
    assert.equal(calls.length, 1);
    assert.equal(calls[0].options, init);
  } finally {
    globalThis.fetch = original;
  }
});

test("read retry keeps the same notice, draft, receipt and prior content until success", async () => {
  await withDOM(async ({ render }) => {
    const { ErrorNotice } = await loadComponent(
      "src/components/ui/error-notice.tsx",
    );
    let resolve, startRefresh,
      attempts = 0;
    function Harness() {
      const [error, setError] = React.useState(responseFailure(503, {}));
      const [pending, setPending] = React.useState(false);
      startRefresh = () => setPending(true);
      return React.createElement(
        "section",
        null,
        React.createElement("input", { defaultValue: "Unsent draft" }),
        React.createElement("span", { "data-receipt": true }, "Receipt.pdf"),
        React.createElement("p", null, "Previously loaded balance"),
        React.createElement(ErrorNotice, {
          error,
          intent: "read",
          resource: "dashboard",
          retained: "view",
          pending,
          onRetry: async () => {
            attempts++;
            setPending(true);
            setError(undefined);
            await new Promise((done) => (resolve = done));
            setPending(false);
          },
        }),
      );
    }
    await render(React.createElement(Harness));
    const frame = document.querySelector(".error-notice"),
      input = document.querySelector("input"),
      receipt = document.querySelector("[data-receipt]"),
      button = frame.querySelector("button");
    await React.act(async () => button.click());
    assert.equal(attempts, 1);
    assert.equal(document.querySelector(".error-notice"), frame);
    assert.equal(frame.dataset.state, "retrying");
    assert.equal(frame.querySelector("button"), button);
    assert.match(frame.textContent, /couldn’t refresh your dashboard/);
    assert.equal(frame.querySelectorAll(".error-notice-copy p").length, 1);
    assert.equal(frame.querySelector("details, summary, .error-notice-title"), null);
    assert.doesNotMatch(frame.textContent, /previous view|still in the form|Response:|Reference:/);
    assert.equal(input.value, "Unsent draft");
    assert.equal(document.querySelector("[data-receipt]"), receipt);
    await React.act(async () => resolve());
    assert.equal(document.querySelector(".error-notice"), null);
    assert.equal(document.querySelector("input"), input);
    await React.act(async () => startRefresh());
    assert.equal(document.querySelector(".error-notice"), null, "a later healthy refresh must not resurrect an old error");
  });
});

test("failed retry is recoverable; writes have no automatic retry; sign-in keeps the original tab", async () => {
  await withDOM(async ({ render }) => {
    const { ErrorNotice } = await loadComponent(
      "src/components/ui/error-notice.tsx",
    );
    await render(
      React.createElement(ErrorNotice, {
        error: responseFailure(503, {}),
        intent: "read",
        onRetry: async () => {
          throw new TypeError("Failed to fetch");
        },
      }),
    );
    await React.act(async () => document.querySelector("button").click());
    assert.match(
      document.querySelector(".error-notice").textContent,
      /couldn’t connect/,
    );
    await render(
      React.createElement(ErrorNotice, {
        error: responseFailure(503, {}, "write"),
        intent: "write",
        onRetry: () => assert.fail("no automatic posting"),
      }),
    );
    assert.equal(document.querySelector("button"), null);
    assert.match(
      document.querySelector('[role="alert"]').textContent,
      /may already be saved/,
    );
    await render(
      React.createElement(ErrorNotice, {
        error: responseFailure(401, {}),
        intent: "read",
      }),
    );
    const link = document.querySelector("a");
    assert.equal(link.target, "_blank");
    assert.equal(link.getAttribute("href"), "/auth/login");
  });
});

test("field errors are associated with the input and motion has a reduced-motion fallback", async () => {
  await withDOM(async ({ render }) => {
    const { Field } = await loadComponent("src/components/form.tsx");
    await render(
      React.createElement(
        Field,
        { label: "Amount", error: "Enter an amount greater than zero." },
        React.createElement("input", { name: "amount" }),
      ),
    );
    const input = document.querySelector("input"),
      error = document.querySelector(".field-error");
    assert.equal(input.getAttribute("aria-invalid"), "true");
    assert.ok(
      input.getAttribute("aria-describedby").split(" ").includes(error.id),
    );
    assert.equal(error.getAttribute("role"), "alert");
  });
  const css = await readFile(
    new URL("../src/components/ui/error-notice.css", import.meta.url),
    "utf8",
  );
  assert.match(css, /prefers-reduced-motion/);
  const inline = css.slice(0, css.indexOf(".startup-error-screen"));
  assert.doesNotMatch(inline, /translate|scale\(|position:\s*(?:fixed|absolute)/);
});

test("contributor guard rejects raw API prose and ad hoc alerts, permits canonical surfaces", async () => {
  const { checkSource } = await import("../scripts/check-error-handling.mjs");
  assert.equal(
    checkSource("components/page.tsx", "throw new Error(data.error)").length,
    1,
  );
  assert.equal(
    checkSource("components/page.tsx", '<p role="alert">Failure</p>').length,
    1,
  );
  assert.equal(
    checkSource("components/page.tsx", '<p className="error">Failure</p>')
      .length,
    1,
  );
  assert.equal(
    checkSource(
      "components/page.tsx",
      '<ErrorNotice error={error} intent="read" />',
    ).length,
    0,
  );
  assert.equal(
    checkSource("components/ui/error-notice.tsx", '<div role="alert"/>').length,
    0,
  );
});


test("body-read cancellation stays silent for reads; cancelled writes remain uncertain", async () => {
  const original = globalThis.fetch;
  const abort = new DOMException("Cancelled after headers", "AbortError");
  try {
    globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => { throw abort; } });
    await assert.rejects(fetchJSON("/read"), error => error === abort);
    await assert.rejects(fetchJSON("/save", { method: "POST", body: "same-payload" }), error => error.failure.outcome === "unknown" && !isConfirmedRejection(error));
    globalThis.fetch = async () => { throw abort; };
    await assert.rejects(fetchJSON("/save", { method: "POST", body: "same-payload" }), error => error.failure.outcome === "unknown");
  } finally { globalThis.fetch = original; }
});


test("debugging codes remain available to code but never appear in the user notice", async () => {
  await withDOM(async ({ render }) => {
    const { ErrorNotice } = await loadComponent(
      "src/components/ui/error-notice.tsx",
    );
    const error = responseFailure(503, { code: "INTERNAL_REFERENCE" }, "write");
    assert.equal(error.status, 503);
    assert.equal(error.failure.code, "INTERNAL_REFERENCE");
    await render(
      React.createElement(ErrorNotice, {
        error,
        intent: "write",
        retained: "form",
      }),
    );
    const notice = document.querySelector(".error-notice");
    assert.equal(notice.querySelectorAll("p").length, 1);
    assert.equal(notice.querySelector("details, summary, h2, h3"), null);
    assert.doesNotMatch(
      notice.textContent,
      /503|INTERNAL_REFERENCE|Technical details|still in the form/,
    );
    assert.equal(
      notice.textContent,
      "Your change may already be saved. Check before trying again.",
    );
  });
});



test("the contributor guard rejects debugging disclosures in shared feedback", async () => {
  const {checkSource} = await import("../scripts/check-error-handling.mjs");
  assert.equal(checkSource("components/ui/error-notice.tsx", '<details>Technical details</details>').length, 1);
});

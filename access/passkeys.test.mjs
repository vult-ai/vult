import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { runInNewContext } from "node:vm";

const script = readFileSync(new URL("./passkeys.js", import.meta.url), "utf8");
const ui = readFileSync(new URL("./ui.js", import.meta.url), "utf8");

// Runs the page script with the shared sign-in helpers. Messages are read as
// text (title, then the next step) and leaving for the Books is a navigation.
function run(context) {
  context.window ??= {};
  runInNewContext(ui, context);
  context.window.vultNotice = (slot, feedback) => {
    if (slot) slot.textContent = feedback ? feedback.title + (feedback.detail ? " " + feedback.detail : "") : "";
  };
  context.window.vultOpen = (destination) => context.location.replace(destination);
  runInNewContext(script, context);
}
const reference = "AAAAAAAAAAAAAAAAAAAAAf____________________4";

async function signIn(destination, signedIn = true) {
  let click;
  const navigations = [];
  const requests = [];
  const button = { id: "passkey-signin", disabled: false, addEventListener: (_, fn) => { click = fn; } };
  const status = { textContent: "" };
  const context = {
    document: {
      getElementById: (id) => id === "passkey-signin" ? button : id === "passkey-status" ? status : null,
      querySelector: () => ({ content: "example-csrf" }),
    },
    location: { hash: "", pathname: "/auth/login", replace: (path) => navigations.push(path) },
    history: { replaceState: () => assert.fail("sign-in changed the URL before confirmation") },
    window: { isSecureContext: true },
    navigator: { credentials: { get: async () => ({ toJSON: () => ({ id: "example", type: "public-key" }) }) } },
    Uint8Array, URLSearchParams, atob, btoa,
    fetch: async (path, options) => {
      requests.push(path);
      assert.equal(options.credentials, "same-origin");
      assert.equal(options.redirect, "error");
      assert.equal(options.headers["X-CSRF-Token"], "example-csrf");
      const body = path.endsWith("/options")
        ? { publicKey: { challenge: "AQ" } }
        : { signed_in: signedIn, continue: destination };
      return { ok: true, headers: { get: () => "application/json" }, json: async () => body };
    },
  };
  run(context);
  assert.equal(typeof click, "function");
  await click();
  assert.deepEqual(requests, ["/auth/passkeys/login/options", "/auth/passkeys/login/verify"]);
  return { navigations, status: status.textContent, disabled: button.disabled };
}

test("actual sign-in callback accepts only supported relative continuations", async () => {
  for (const destination of ["/", `/oauth/authorize?request=${reference}`, `/auth/approve?operation=${reference}`, "/auth/recovery-codes", `/auth/agent?request=${"A".repeat(43)}`]) {
    const result = await signIn(destination);
    assert.deepEqual(result.navigations, [destination]);
    // Leaving for the next step replaces the page; no message is left behind.
    assert.equal(result.status, "Waiting for your device…");
  }
});

test("actual sign-in callback rejects substituted, ambiguous and noncanonical destinations", async () => {
  for (const destination of [
    undefined, null, 17, {}, "https://example.com/", "//example.com/", "/auth/approve",
    `https://ledger.example.com/auth/approve?operation=${reference}`,
    `/auth/approve?operation=${reference}#fragment`,
    `/auth/approve?operation=${reference}&operation=${reference}`,
    `/auth/approve?operation=${reference}&return_to=/`,
    `/auth/approve?operation=${reference.slice(0, -1)}5`,
    `/auth/approve?operation=${reference}%0a`,
    "/auth/recovery-codes?next=/", `/auth/agent?request=${"A".repeat(42)}`, `/auth/agent?request=${"A".repeat(43)}&x=1`,
  ]) {
    const result = await signIn(destination);
    assert.deepEqual(result.navigations, []);
    assert.equal(result.disabled, false);
    assert.equal(result.status, "Sign-in wasn’t confirmed Try your passkey again.");
  }
  const result = await signIn(`/auth/approve?operation=${reference}`, false);
  assert.deepEqual(result.navigations, []);
});

async function register({ hash = "", code = "", recovering = false, local = false, failOptions = false, failFirstOptions = false, failCredential = false, failFirstCredential = false, failVerify = false } = {}) {
  let click;
  let rejectedOptions = false;
  let credentialAttempts = 0;
  const navigations = [], requests = [], removedFragments = [];
  const button = { id: recovering ? "passkey-recover" : "passkey-register", disabled: false, addEventListener: (_, fn) => { click = fn; } };
  const status = { textContent: "" }, input = { value: code }, label = { hidden: true };
  const nodes = { [button.id]: button, "passkey-status": status, ...(recovering
    ? { "passkey-recovery-code": { value: "A".repeat(22) }, "passkey-code-label": { hidden: false } }
    : { "passkey-setup-code": input, "passkey-setup-label": label }) };
  const dial = { dataset: { phase: "idle" } };
  if (local) Object.assign(nodes, { "passkey-dial": dial, "passkey-setup-missing": { hidden: true } });
  const context = {
    document: { getElementById: (id) => nodes[id] || null, querySelector: () => ({ content: "example-csrf" }) },
    location: { hash, pathname: "/auth/setup", replace: (path) => navigations.push(path) },
    history: { replaceState: (_, __, path) => removedFragments.push(path) },
    window: { isSecureContext: true, PublicKeyCredential: function PublicKeyCredential() {} },
    navigator: { credentials: { create: async () => {
      assert.equal(input.value, "", "setup input retained the code during credential creation");
      if (failCredential || (failFirstCredential && credentialAttempts++ === 0)) throw new Error("canceled");
      return { toJSON: () => ({ id: "example", type: "public-key" }) };
    } } },
    Uint8Array, URLSearchParams, atob, btoa, setTimeout: (fn) => fn(),
    fetch: async (path, options) => {
      requests.push({ path, body: JSON.parse(options.body) });
      assert.equal(options.credentials, "same-origin");
      assert.equal(options.redirect, "error");
      assert.equal(options.headers["X-CSRF-Token"], "example-csrf");
      assert.ok(!path.includes(reference));
      if (path.endsWith("/options") && failFirstOptions && !rejectedOptions) {
        rejectedOptions = true;
        return { ok: false, status: 401, headers: { get: () => "application/json" } };
      }
      if (path.endsWith("/options") && failOptions) throw new Error("lost reply");
      if (path.endsWith("/verify") && failVerify) throw new Error("lost reply after verification");
      const body = path.endsWith("/recover") ? { setup_token: reference }
        : path.endsWith("/options") ? { publicKey: { challenge: "AQ" } } : { signed_in: true, continue: "/" };
      return { ok: true, headers: { get: () => "application/json" }, json: async () => body };
    },
  };
  run(context);
  const initiallyHidden = label.hidden;
  await click();
  return { requests, navigations, removedFragments, input, label, status, button, initiallyHidden, click, dial };
}

test("manual setup uses the existing CSRF-checked registration without a secret URL", async () => {
  const result = await register({ code: reference });
  assert.equal(result.initiallyHidden, false);
  assert.deepEqual(result.removedFragments, []);
  assert.deepEqual(result.requests, [
    { path: "/auth/passkeys/register/options", body: { setup_token: reference } },
    { path: "/auth/passkeys/register/verify", body: { credential: { id: "example", type: "public-key" } } },
  ]);
  assert.deepEqual(result.navigations, ["/"]);
  assert.equal(result.input.value, "");
  assert.equal(result.label.hidden, true);
  assert.equal(result.status.textContent, "Waiting for your device…");
});

test("a valid setup fragment keeps precedence over a different manual code", async () => {
  const result = await register({ hash: `#setup=${reference}`, code: "B".repeat(43) });
  assert.equal(result.initiallyHidden, true);
  assert.deepEqual(result.removedFragments, ["/auth/setup"]);
  assert.equal(result.requests[0].body.setup_token, reference);
  assert.deepEqual(result.navigations, ["/"]);
});

test("invalid manual setup codes make no registration requests and remain correctable", async () => {
  for (const code of ["", "a".repeat(42), "a".repeat(44), `${"a".repeat(42)}+`, `${"a".repeat(20)} ${"a".repeat(22)}`]) {
    const result = await register({ code });
    assert.deepEqual(result.requests, []);
    assert.deepEqual(result.navigations, []);
    assert.equal(result.button.disabled, false);
    assert.equal(result.label.hidden, false);
    result.input.value = reference;
    await result.click();
    assert.deepEqual(result.navigations, ["/"]);
  }
});

test("a failed setup request does not navigate or invent a completed sign-in", async () => {
  for (const options of [{ failOptions: true }, { failCredential: true }, { failVerify: true }]) {
    const result = await register({ code: reference, ...options });
    assert.deepEqual(result.navigations, []);
    assert.equal(result.button.disabled, false);
    assert.ok(!result.status.textContent.includes(reference));
    assert.equal(result.status.textContent, "Passkey setup wasn’t confirmed Try signing in with your passkey, or request a fresh setup code.");
  }
});

test("rejected manual and fragment codes can be replaced after a real options rejection", async () => {
  for (const initial of [{ code: reference }, { hash: `#setup=${reference}` }]) {
    const result = await register({ ...initial, failFirstOptions: true });
    assert.equal(result.requests.length, 1);
    assert.equal(result.requests[0].body.setup_token, reference);
    assert.equal(result.label.hidden, false);
    assert.equal(result.button.disabled, false);
    assert.deepEqual(result.navigations, []);
    const replacement = "A".repeat(43);
    result.input.value = replacement;
    await result.click();
    assert.equal(result.requests[1].body.setup_token, replacement);
    assert.deepEqual(result.navigations, ["/"]);
    assert.equal(result.input.value, "");
  }
});

test("recovery retains its memory-only setup when replacement credential creation is canceled", async () => {
  const result = await register({ recovering: true, failFirstCredential: true });
  assert.deepEqual(result.requests.map((request) => request.path), ["/auth/passkeys/recover"]);
  await result.click();
  assert.deepEqual(result.navigations, []);
  assert.equal(result.button.disabled, false);
  await result.click();
  assert.deepEqual(result.requests.map((request) => request.path), [
    "/auth/passkeys/recover", "/auth/passkeys/register/options",
    "/auth/passkeys/register/options", "/auth/passkeys/register/verify",
  ]);
  assert.equal(result.requests[1].body.setup_token, reference);
  assert.equal(result.requests[2].body.setup_token, reference);
  assert.deepEqual(result.navigations, ["/"]);
});

async function codeSignIn(value, reply) {
  let submit;
  const navigations = [];
  const requests = [];
  const form = { addEventListener: (_, fn) => { submit = fn; } };
  const input = { value };
  const status = { textContent: "" };
  const elements = { "authenticator-signin": form, "authenticator-code": input, "authenticator-status": status };
  const context = {
    document: { getElementById: (id) => elements[id] || null, querySelector: () => ({ content: "example-csrf" }) },
    location: { hash: "", pathname: "/auth/login", replace: (path) => navigations.push(path) },
    history: { replaceState: () => {} },
    window: { isSecureContext: true },
    Uint8Array, URLSearchParams, atob, btoa,
    fetch: async (path, options) => {
      requests.push({ path, body: JSON.parse(options.body) });
      assert.equal(options.headers["X-CSRF-Token"], "example-csrf");
      return { ok: reply.status === 200, headers: { get: () => "application/json" }, json: async () => reply.body };
    },
  };
  run(context);
  assert.equal(typeof submit, "function");
  await submit({ preventDefault: () => {} });
  return { navigations, requests, status: status.textContent, input };
}

test("an authenticator code signs in and follows only allowed continuations", async () => {
  const signedIn = await codeSignIn("123 456", { status: 200, body: { signed_in: true, continue: "/auth/agent?request=" + reference } });
  assert.deepEqual(signedIn.requests, [{ path: "/auth/authenticator/login", body: { code: "123456" } }]);
  assert.deepEqual(signedIn.navigations, ["/auth/agent?request=" + reference]);
  const elsewhere = await codeSignIn("123456", { status: 200, body: { signed_in: true, continue: "https://example.com/" } });
  assert.deepEqual(elsewhere.navigations, []);
});

test("an authenticator code is checked before sending and wrong codes are explained", async () => {
  const short = await codeSignIn("12345", { status: 200, body: {} });
  assert.deepEqual(short.requests, []);
  assert.match(short.status, /6-digit code/);
  const wrong = await codeSignIn("654321", { status: 400, body: { error: "invalid_code" } });
  assert.deepEqual(wrong.navigations, []);
  assert.equal(wrong.input.value, "");
  assert.match(wrong.status, /Code not accepted/);
});

test("setup can switch to an authenticator app and continues to the recovery codes", async () => {
  const listeners = {};
  const element = (id, extra = {}) => ({ id, hidden: false, disabled: false, textContent: "", value: "",
    addEventListener: (type, fn) => { listeners[id + ":" + type] = fn; }, replaceChildren: () => {}, ...extra });
  const elements = Object.fromEntries(["passkey-register", "passkey-status", "authenticator-switch", "authenticator-setup",
    "authenticator-qr", "authenticator-secret", "authenticator-copy", "authenticator-code", "authenticator-verify",
    "authenticator-status"].map((id) => [id, element(id)]));
  elements["authenticator-switch"].hidden = true;
  elements["authenticator-setup"].hidden = true;
  const requests = [];
  const navigations = [];
  const context = {
    document: { getElementById: (id) => elements[id] || null, querySelector: () => ({ content: "example-csrf" }) },
    location: { hash: "#setup=" + reference, pathname: "/auth/setup", replace: (path) => navigations.push(path) },
    history: { replaceState: () => {} },
    sessionStorage: { setItem: () => {}, getItem: () => null, removeItem: () => {} },
    window: { isSecureContext: true },
    Uint8Array, URLSearchParams, atob, btoa,
    fetch: async (path, options) => {
      const body = JSON.parse(options.body);
      requests.push({ path, body });
      const reply = path.endsWith("/options") ? { secret: "JBSWY3DPEHPK3PXP", uri: "otpauth://totp/Vult", qr: "" }
        : body.code === "111111" ? { error: "invalid_code" } : { signed_in: true, continue: "/auth/recovery-codes" };
      return { ok: !reply.error, headers: { get: () => "application/json" }, json: async () => reply };
    },
  };
  run(context);
  assert.equal(elements["authenticator-switch"].hidden, false);
  await listeners["authenticator-switch:click"]();
  assert.equal(elements["authenticator-setup"].hidden, false);
  assert.equal(elements["passkey-register"].hidden, true);
  assert.equal(elements["authenticator-secret"].textContent, "JBSWY3DPEHPK3PXP");
  elements["authenticator-code"].value = "111111";
  await listeners["authenticator-verify:click"]();
  assert.match(elements["authenticator-status"].textContent, /Code not accepted/);
  assert.deepEqual(navigations, []);
  elements["authenticator-code"].value = "123456";
  await listeners["authenticator-verify:click"]();
  assert.deepEqual(requests.map((r) => r.path), ["/auth/authenticator/enroll/options",
    "/auth/authenticator/enroll/verify", "/auth/authenticator/enroll/verify"]);
  assert.equal(requests[0].body.setup_token, reference);
  assert.deepEqual(navigations, ["/auth/recovery-codes"]);
});

test("on this Mac a failed passkey keeps the setup link and never asks for a code", async () => {
  const result = await register({ local: true, hash: `#setup=${reference}`, failFirstCredential: true });
  assert.equal(result.label.hidden, true);
  assert.equal(result.status.textContent, "Passkey not created Try again, or use an authenticator app.");
  assert.equal(result.dial.dataset.phase, "idle");
  assert.equal(result.button.disabled, false);
  await result.click();
  assert.equal(result.label.hidden, true);
  assert.deepEqual(result.requests.map((r) => r.body.setup_token).filter(Boolean), [reference, reference]);
  assert.equal(result.dial.dataset.phase, "open");
  assert.equal(result.status.textContent, "Passkey created");
  assert.deepEqual(result.navigations, ["/"]);
});

test("the authenticator gets its own screen and the passkey stays one step back", async () => {
  let switchClick, backClick;
  const elements = Object.fromEntries(["passkey-register", "passkey-status", "authenticator-switch", "authenticator-setup", "passkey-threshold",
    "authenticator-secret", "authenticator-code", "authenticator-status", "authenticator-back"].map((id) => [id, { id, hidden: id === "authenticator-setup" || id === "authenticator-switch", textContent: "", value: "", focus() {} }]));
  elements["authenticator-switch"].addEventListener = (_, fn) => { switchClick = fn; };
  elements["authenticator-back"].addEventListener = (_, fn) => { backClick = fn; };
  elements["passkey-register"].addEventListener = () => {};
  elements["authenticator-code"].addEventListener = () => {};
  runInNewContext(script, {
    document: { getElementById: (id) => elements[id] || null, querySelector: () => ({ content: "example-csrf" }) },
    location: { hash: `#setup=${reference}`, pathname: "/auth/setup" },
    history: { replaceState: () => {} },
    window: { isSecureContext: true },
    sessionStorage: { setItem() {}, getItem: () => null, removeItem() {} },
    Uint8Array, URLSearchParams, atob, btoa, setTimeout,
    fetch: async () => ({ ok: true, headers: { get: () => "application/json" }, json: async () => ({ secret: "JBSWY3DPEHPK3PXP" }) }),
  });
  assert.equal(elements["authenticator-switch"].hidden, false);
  await switchClick();
  assert.equal(elements["passkey-threshold"].hidden, true);
  assert.equal(elements["authenticator-setup"].hidden, false);
  assert.equal(elements["authenticator-secret"].textContent, "JBSWY3DPEHPK3PXP");
  assert.equal(elements["passkey-register"].hidden, false, "the passkey button stays ready behind the screen");
  elements["authenticator-code"].value = "123";
  backClick();
  assert.equal(elements["passkey-threshold"].hidden, false);
  assert.equal(elements["authenticator-setup"].hidden, true);
  assert.equal(elements["authenticator-code"].value, "");
  assert.equal(elements["authenticator-switch"].disabled, false);
});

test("without passkeys on this device the authenticator leads, as on Vult's sign-up", () => {
  const elements = Object.fromEntries(["passkey-register", "passkey-status", "passkey-dial", "passkey-fine", "passkey-setup-missing",
    "authenticator-switch", "authenticator-setup", "setup-help-toggle"].map((id) => [id, { id, hidden: false, disabled: false, textContent: "", className: "link", dataset: {}, addEventListener() {} }]));
  elements["authenticator-switch"].hidden = true;
  runInNewContext(script, {
    document: { getElementById: (id) => elements[id] || null, querySelector: () => ({ content: "example-csrf" }) },
    location: { hash: `#setup=${reference}`, pathname: "/auth/setup" },
    history: { replaceState: () => {} },
    window: { isSecureContext: true },
    navigator: {},
    sessionStorage: { setItem() {}, getItem: () => null, removeItem() {} },
    Uint8Array, URLSearchParams, atob, btoa,
  });
  assert.equal(elements["passkey-register"].disabled, true);
  assert.equal(elements["passkey-fine"].textContent, "Passkeys aren’t available here. Use an authenticator app, or open Vult on another device.");
  assert.equal(elements["authenticator-switch"].hidden, false);
  assert.equal(elements["authenticator-switch"].textContent, "Use an authenticator app");
  assert.equal(elements["authenticator-switch"].className, "secondary wide");
});

"use strict";

const say = (slot, feedback) => window.vultNotice?.(slot, feedback);

(() => {
  const button = document.getElementById("passkey-register") || document.getElementById("passkey-signin") || document.getElementById("passkey-recover");
  const status = document.getElementById("passkey-status");
  // The local setup page's passkey dial follows the ceremony.
  const dial = document.getElementById("passkey-dial");
  const csrf = document.querySelector('meta[name="vult-login-csrf"]')?.content;
  if (!button || !status || !csrf) return;
  let recovering = button.id === "passkey-recover";
  let registering = button.id === "passkey-register";
  const fragment = new URLSearchParams(location.hash.slice(1));
  let setup = fragment.getAll("setup").length === 1 ? fragment.get("setup") : "";
  if (!/^[A-Za-z0-9_-]{43}$/.test(setup || "")) setup = "";
  // Keep a setup link's one-use code for this tab, so a reload doesn't lose it.
  const setupKey = "vult-setup-code";
  try {
    if (registering && setup) sessionStorage.setItem(setupKey, setup);
    else if (registering) {
      const kept = sessionStorage.getItem(setupKey) || "";
      if (/^[A-Za-z0-9_-]{43}$/.test(kept)) setup = kept;
    }
  } catch { /* Without storage the link must be opened again. */ }
  if (location.hash) history.replaceState(null, "", location.pathname);
  const setupInput = document.getElementById("passkey-setup-code");
  const setupLabel = document.getElementById("passkey-setup-label");
  const setupMissing = document.getElementById("passkey-setup-missing");
  if (registering && !/^[A-Za-z0-9_-]{43}$/.test(setup || "")) {
    // On this Mac there is no code to type: the owner opens setup again.
    if (setupMissing) {
      setupMissing.hidden = false;
      button.hidden = true;
    } else if (setupLabel) setupLabel.hidden = false;
  }

  // On this Mac the owner may set up an authenticator app instead.
  const authSwitch = document.getElementById("authenticator-switch");
  const authSetup = document.getElementById("authenticator-setup");
  if (registering && authSwitch && authSetup && /^[A-Za-z0-9_-]{43}$/.test(setup || "")) {
    const qr = document.getElementById("authenticator-qr");
    const secret = document.getElementById("authenticator-secret");
    const copy = document.getElementById("authenticator-copy");
    const code = document.getElementById("authenticator-code");
    const verify = document.getElementById("authenticator-verify");
    const authStatus = document.getElementById("authenticator-status");
    authSwitch.hidden = false;
    authSwitch.addEventListener("click", async () => {
      if (authSwitch.disabled) return;
      authSwitch.disabled = true;
      say(status, null);
      try {
        const enrollment = await request("/auth/authenticator/enroll/options", { setup_token: setup });
        if (typeof enrollment.secret !== "string" || !/^[A-Z2-7]+$/.test(enrollment.secret)) throw new Error("No key");
        if (qr && typeof enrollment.qr === "string" && typeof DOMParser === "function") {
          const svg = new DOMParser().parseFromString(enrollment.qr, "image/svg+xml").documentElement;
          if (svg && svg.nodeName === "svg") qr.replaceChildren(svg);
        }
        if (secret) secret.textContent = enrollment.secret;
        // The passkey screen gives way to its own authenticator screen.
        const threshold = document.getElementById("passkey-threshold");
        if (threshold) threshold.hidden = true;
        else {
          button.hidden = true;
          const fine = document.getElementById("passkey-fine");
          if (fine) fine.hidden = true;
          authSwitch.hidden = true;
        }
        authSetup.hidden = false;
        code?.focus?.();
      } catch {
        say(status, { title: "Couldn’t start authenticator setup", detail: "Close this tab and run vult setup again, or ask your agent to." });
        authSwitch.disabled = false;
      }
    });
    copy?.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(secret?.textContent || "");
        say(authStatus, { title: "Setup key copied", tone: "success" });
        if (copy?.dataset) {
          copy.dataset.copied = "";
          setTimeout(() => { delete copy.dataset.copied; }, 2000);
        }
      } catch {
        say(authStatus, { title: "Couldn’t copy", detail: "Select the setup key and copy it." });
      }
    });
    let checking = false;
    // Six boxes over one real input, as on Vult's sign-up: the input keeps
    // typing, pasting and one-time-code autofill; the boxes draw it.
    const slots = Array.from(document.getElementById("authenticator-slots")?.children || []);
    const otp = slots[0]?.parentElement?.parentElement;
    const verifying = document.getElementById("authenticator-verifying");
    const digits = () => (code?.value || "").replace(/\D/g, "").slice(0, 6);
    const paint = () => {
      if (!code) return;
      const value = digits();
      if (code.value !== value) code.value = value;
      const active = document.activeElement === code ? Math.min(value.length, 5) : -1;
      slots.forEach((slot, i) => {
        slot.textContent = value[i] || "";
        if (i === active) slot.setAttribute("data-active", "");
        else slot.removeAttribute("data-active");
      });
      if (verify) verify.disabled = checking || value.length !== 6;
    };
    const busy = (on) => {
      checking = on;
      if (verifying) verifying.hidden = !on;
      if (verify) verify.textContent = on ? "Verifying…" : "Verify and continue";
      if (code) code.disabled = on;
      paint();
    };
    const confirm = async () => {
      if (checking || !code || !authStatus) return;
      const value = digits();
      if (!/^[0-9]{6}$/.test(value)) {
        say(authStatus, { title: "Enter the 6-digit code", detail: "Use the latest code from your authenticator app." });
        return;
      }
      say(authStatus, null);
      busy(true);
      try {
        const response = await fetch("/auth/authenticator/enroll/verify", {
          method: "POST", credentials: "same-origin", redirect: "error", cache: "no-store",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
          body: JSON.stringify({ code: value }),
        });
        const result = response.headers.get("Content-Type")?.includes("application/json") ? await response.json() : {};
        if (!response.ok) {
          code.value = "";
          otp?.setAttribute("data-invalid", "");
          say(authStatus, result.error === "invalid_code"
            ? { title: "Code not accepted", detail: "Enter the latest code from your authenticator app. If it keeps failing, check that your phone sets its time automatically.", invalidCode: true }
            : result.error === "too_many_attempts" ? { title: "Too many attempts", detail: "Wait five minutes, then try again.", tone: "warning" }
            : { title: "Setup wasn’t confirmed", detail: "Close this tab and run vult setup again, or ask your agent to." });
        } else if (result.signed_in === true && window.vultContinuation?.(result.continue)) {
          setup = "";
          try { sessionStorage.removeItem(setupKey); } catch { /* Nothing kept. */ }
          // The recovery codes come next; the Books open after them.
          if (result.continue === "/auth/recovery-codes") location.replace(result.continue);
          else window.vultOpen(result.continue);
          return;
        } else {
          say(authStatus, { title: "Setup wasn’t confirmed", detail: "Close this tab and run vult setup again, or ask your agent to." });
        }
      } catch {
        say(authStatus, { title: "Setup wasn’t confirmed", detail: "Check your connection and try again." });
      }
      busy(false);
      code.focus?.();
    };
    verify?.addEventListener("click", confirm);
    code?.addEventListener("keydown", (event) => { if (event.key === "Enter") confirm(); });
    // The sixth digit checks the code, as on Vult's sign-up.
    code?.addEventListener("input", () => {
      // Editing a rejected code clears its message.
      if (otp?.hasAttribute("data-invalid")) say(authStatus, null);
      otp?.removeAttribute("data-invalid");
      paint();
      if (digits().length === 6) confirm();
    });
    code?.addEventListener("focus", paint);
    code?.addEventListener("blur", paint);
    // Back to the passkey: the unfinished authenticator can't sign anyone in.
    document.getElementById("authenticator-back")?.addEventListener("click", () => {
      authSetup.hidden = true;
      const threshold = document.getElementById("passkey-threshold");
      if (threshold) threshold.hidden = false;
      if (code) code.value = "";
      say(authStatus, null);
      paint();
      authSwitch.disabled = false;
      button.focus?.();
    });
  }

  // As on Vult's sign-up: without passkeys here, the authenticator leads; while
  // the passkey prompt is open, the other ways wait.
  const helpToggle = document.getElementById("setup-help-toggle");
  const signInLinks = document.querySelector("#passkey-threshold .links");
  const others = [authSwitch, helpToggle, signInLinks].filter(Boolean);
  const shownOthers = () => others.filter((node) => !node.hidden);
  if (dial && registering && !(window.isSecureContext && window.PublicKeyCredential && navigator.credentials?.create)) {
    button.disabled = true;
    const fine = document.getElementById("passkey-fine");
    if (fine) fine.textContent = "Passkeys aren’t available here. Use an authenticator app, or open Vult on another device.";
    if (authSwitch) {
      authSwitch.textContent = "Use an authenticator app";
      authSwitch.className = "secondary wide";
    }
  }

  const decode = (value) => Uint8Array.from(atob(value.replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
  const encode = (value) => btoa(String.fromCharCode(...new Uint8Array(value))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  async function request(path, body) {
    const response = await fetch(path, {
      method: "POST", credentials: "same-origin", redirect: "error", cache: "no-store",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(body),
    });
    if (!response.ok || !response.headers.get("Content-Type")?.includes("application/json")) throw new Error("Passkey request failed");
    return response.json();
  }
  function optionsFromJSON(options) {
    const value = { ...options.publicKey, challenge: decode(options.publicKey.challenge) };
    if (value.user) value.user = { ...value.user, id: decode(value.user.id) };
    for (const field of ["allowCredentials", "excludeCredentials"])
      if (value[field]) value[field] = value[field].map((credential) => ({ ...credential, id: decode(credential.id) }));
    return { publicKey: value };
  }
  function credentialJSON(credential) {
    if (typeof credential.toJSON === "function") return credential.toJSON();
    const response = { clientDataJSON: encode(credential.response.clientDataJSON) };
    if (registering) {
      response.attestationObject = encode(credential.response.attestationObject);
      if (credential.response.getTransports) response.transports = credential.response.getTransports();
    } else {
      response.authenticatorData = encode(credential.response.authenticatorData);
      response.signature = encode(credential.response.signature);
      if (credential.response.userHandle) response.userHandle = encode(credential.response.userHandle);
    }
    return { id: credential.id, rawId: encode(credential.rawId), type: credential.type,
      authenticatorAttachment: credential.authenticatorAttachment, response,
      clientExtensionResults: credential.getClientExtensionResults() };
  }
  button.addEventListener("click", async () => {
    if (button.disabled) return;
    if (recovering) {
      const input = document.getElementById("passkey-recovery-code");
      if (!window.isSecureContext || !input || !/^[A-Za-z0-9_-]{22}$/.test(input.value)) {
        say(status, { title: "Enter a recovery code", detail: "Use one of your saved codes at this installation’s secure address." });
        return;
      }
      button.disabled = true;
      say(status, { title: "Checking your recovery code…", tone: "neutral" });
      try {
        const result = await request("/auth/passkeys/recover", { code: input.value });
        if (!/^[A-Za-z0-9_-]{43}$/.test(result.setup_token || "")) throw new Error("Recovery was not confirmed");
        setup = result.setup_token;
        input.value = "";
        document.getElementById("passkey-code-label").hidden = true;
        recovering = false;
        registering = true;
        button.textContent = "Create replacement passkey";
        say(status, { title: "Code accepted", detail: "Your old sign-ins and passkeys are revoked. Create a replacement passkey to sign in.", tone: "success" });
      } catch {
        say(status, { title: "Code not accepted", detail: "If you already used this code, try another saved code." });
      }
      button.disabled = false;
      return;
    }
    if (registering && !setup && setupInput) setup = setupInput.value.trim();
    if (!window.isSecureContext || !navigator.credentials || (registering && !/^[A-Za-z0-9_-]{43}$/.test(setup || ""))) {
      if (registering) setup = "";
      say(status, registering
        ? { title: "Setup code needed", detail: "Enter a valid setup code, or use a fresh setup link." }
        : { title: "Passkeys need a secure address", detail: "Open this installation’s secure address to use a passkey." });
      return;
    }
    button.disabled = true;
    const waiting = dial ? shownOthers() : [];
    for (const node of waiting) node.hidden = true;
    if (dial) dial.dataset.phase = "reading";
    say(status, { title: "Waiting for your device…", tone: "neutral" });
    const path = registering ? "/auth/passkeys/register" : "/auth/passkeys/login";
    try {
      const options = await request(path + "/options", registering ? { setup_token: setup } : {});
      if (registering && setupInput) setupInput.value = "";
      if (registering && setupLabel) setupLabel.hidden = true;
      const credential = registering ? await navigator.credentials.create(optionsFromJSON(options)) : await navigator.credentials.get(optionsFromJSON(options));
      if (!credential) throw new Error("Passkey was not used");
      const result = await request(path + "/verify", { credential: credentialJSON(credential) });
      if (result.signed_in !== true || !window.vultContinuation?.(result.continue)) throw new Error("Sign-in was not confirmed");
      setup = "";
      try { sessionStorage.removeItem(setupKey); } catch { /* Nothing kept. */ }
      if (dial) {
        dial.dataset.phase = "open";
        // Setup says what it made; sign-in lets the dial open on its own.
        say(status, registering ? { title: "Passkey created", tone: "success" } : null);
        await new Promise((resolve) => setTimeout(resolve, registering ? 900 : 600));
      }
      if (result.continue.startsWith("/auth/") || result.continue.startsWith("/oauth/")) location.replace(result.continue);
      else window.vultOpen(result.continue);
    } catch {
      if (dial) dial.dataset.phase = "idle";
      for (const node of waiting) node.hidden = false;
      // On this Mac the setup link's code stays for another try; there is
      // never a code to type. A server asks for a fresh one.
      if (registering && setupInput && setupLabel && !setupMissing) {
        setupInput.value = setup;
        setupLabel.hidden = false;
        setup = "";
      }
      say(status, registering && setupMissing
        ? { title: "Passkey not created", detail: "Try again, or use an authenticator app.", tone: "neutral" }
        : registering ? { title: "Passkey setup wasn’t confirmed", detail: "Try signing in with your passkey, or request a fresh setup code." }
        : { title: "Sign-in wasn’t confirmed", detail: document.getElementById("authenticator-signin-switch") ? "Try your passkey again, or use your authenticator app." : "Try your passkey again." });
      button.disabled = false;
    }
  });
})();

// On the sign-in page an authenticator app is the other way in: it has its
// own screen, and the passkey is one step back.
(() => {
  const open = document.getElementById("authenticator-signin-switch");
  const back = document.getElementById("authenticator-signin-back");
  const threshold = document.getElementById("passkey-threshold");
  const form = document.getElementById("authenticator-signin");
  if (!open || !back || !threshold || !form) return;
  open.addEventListener("click", () => {
    threshold.hidden = true;
    form.hidden = false;
    document.getElementById("authenticator-code")?.focus?.();
  });
  back.addEventListener("click", () => {
    form.hidden = true;
    threshold.hidden = false;
    document.getElementById("passkey-signin")?.focus?.();
  });
})();

// Sign in with a code from an authenticator app, when the owner has one.
(() => {
  const form = document.getElementById("authenticator-signin");
  const input = document.getElementById("authenticator-code");
  const status = document.getElementById("authenticator-status");
  const csrf = document.querySelector('meta[name="vult-login-csrf"]')?.content;
  if (!form || !input || !status || !csrf) return;
  let busy = false;
  // Six cells over the real input, as on every Vult code entry; the sixth
  // digit signs in.
  const slots = Array.from(document.getElementById("authenticator-slots")?.children || []);
  const otp = document.getElementById("authenticator-otp");
  const digits = () => (input.value || "").replace(/\D/g, "").slice(0, 6);
  const paint = () => {
    if (input.value !== digits()) input.value = digits();
    const active = document.activeElement === input ? Math.min(input.value.length, 5) : -1;
    slots.forEach((slot, i) => {
      slot.textContent = input.value[i] || "";
      if (i === active) slot.setAttribute("data-active", "");
      else slot.removeAttribute("data-active");
    });
  };
  input.addEventListener?.("focus", paint);
  input.addEventListener?.("blur", paint);
  input.addEventListener?.("input", () => {
    // Editing a rejected code clears its message.
    if (otp?.hasAttribute("data-invalid")) say(status, null);
    otp?.removeAttribute("data-invalid");
    paint();
    if (digits().length === 6) form.requestSubmit?.();
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (busy) return;
    const code = input.value.replace(/\s+/g, "");
    if (!/^[0-9]{6}$/.test(code)) {
      say(status, { title: "Enter the 6-digit code", detail: "Use the latest code from your authenticator app." });
      return;
    }
    busy = true;
    say(status, { title: "Checking your code…", tone: "neutral" });
    try {
      const response = await fetch("/auth/authenticator/login", {
        method: "POST", credentials: "same-origin", redirect: "error", cache: "no-store",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ code }),
      });
      const json = response.headers.get("Content-Type")?.includes("application/json") ? await response.json() : {};
      if (!response.ok) {
        input.value = "";
        otp?.setAttribute("data-invalid", "");
        paint();
        say(status, json.error === "invalid_code" ? { title: "Code not accepted", detail: "Enter the latest code from your authenticator app." }
          : json.error === "too_many_attempts" ? { title: "Too many attempts", detail: "Wait five minutes, then try again.", tone: "warning" }
          : { title: "Sign-in wasn’t confirmed", detail: "Reload this page and try again." });
        busy = false;
        return;
      }
      if (json.signed_in !== true || !window.vultContinuation?.(json.continue)) throw new Error("Sign-in was not confirmed");
      if (json.continue.startsWith("/auth/") || json.continue.startsWith("/oauth/")) location.replace(json.continue);
      else window.vultOpen(json.continue);
    } catch {
      say(status, { title: "Sign-in wasn’t confirmed", detail: "Reload this page and try again." });
      busy = false;
    }
  });
})();

// "Having trouble?" on the local setup page shows what else to try.
(() => {
  const toggle = document.getElementById("setup-help-toggle");
  const help = document.getElementById("setup-help");
  if (!toggle || !help) return;
  toggle.addEventListener("click", () => {
    help.hidden = !help.hidden;
    toggle.setAttribute("aria-expanded", String(!help.hidden));
  });
})();

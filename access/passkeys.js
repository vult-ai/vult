"use strict";

(() => {
  const button = document.getElementById("passkey-register") || document.getElementById("passkey-signin") || document.getElementById("passkey-recover");
  const status = document.getElementById("passkey-status");
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
      status.textContent = "";
      try {
        const enrollment = await request("/auth/authenticator/enroll/options", { setup_token: setup });
        if (typeof enrollment.secret !== "string" || !/^[A-Z2-7]+$/.test(enrollment.secret)) throw new Error("No key");
        if (qr && typeof enrollment.qr === "string" && typeof DOMParser === "function") {
          const svg = new DOMParser().parseFromString(enrollment.qr, "image/svg+xml").documentElement;
          if (svg && svg.nodeName === "svg") qr.replaceChildren(svg);
        }
        if (secret) secret.textContent = enrollment.secret;
        button.hidden = true;
        const fine = document.getElementById("passkey-fine");
        if (fine) fine.hidden = true;
        authSwitch.hidden = true;
        authSetup.hidden = false;
        code?.focus?.();
      } catch {
        status.textContent = "Couldn't start authenticator setup. Close this tab and run vult setup again, or ask your agent to.";
        authSwitch.disabled = false;
      }
    });
    copy?.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(secret?.textContent || "");
        if (authStatus) authStatus.textContent = "Key copied.";
      } catch {
        if (authStatus) authStatus.textContent = "Couldn't copy. Select the key and copy it by hand.";
      }
    });
    let checking = false;
    const confirm = async () => {
      if (checking || !code || !authStatus) return;
      const value = code.value.replace(/\s+/g, "");
      if (!/^[0-9]{6}$/.test(value)) {
        authStatus.textContent = "Enter the 6-digit code from your authenticator app.";
        return;
      }
      checking = true;
      if (verify) verify.disabled = true;
      authStatus.textContent = "Checking your code…";
      try {
        const response = await fetch("/auth/authenticator/enroll/verify", {
          method: "POST", credentials: "same-origin", redirect: "error", cache: "no-store",
          headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
          body: JSON.stringify({ code: value }),
        });
        const result = response.headers.get("Content-Type")?.includes("application/json") ? await response.json() : {};
        if (!response.ok) {
          code.value = "";
          authStatus.textContent = result.error === "invalid_code" ? "That code didn't work. Enter the newest code from your authenticator app. If it keeps failing, check that your phone's clock is set automatically."
            : result.error === "too_many_attempts" ? "Too many codes were tried. Wait five minutes, then try again."
            : "Setup was not confirmed. Close this tab and run vult setup again, or ask your agent to.";
        } else if (result.signed_in === true && (result.continue === "/auth/recovery-codes" || result.continue === "/")) {
          setup = "";
          try { sessionStorage.removeItem(setupKey); } catch { /* Nothing kept. */ }
          authStatus.textContent = "Authenticator set up. Continuing…";
          location.replace(result.continue);
          return;
        } else {
          authStatus.textContent = "Setup was not confirmed. Close this tab and run vult setup again, or ask your agent to.";
        }
      } catch {
        authStatus.textContent = "Setup was not confirmed. Check your connection and try again.";
      }
      checking = false;
      if (verify) verify.disabled = false;
    };
    verify?.addEventListener("click", confirm);
    code?.addEventListener("keydown", (event) => { if (event.key === "Enter") confirm(); });
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
        status.textContent = "Enter a saved recovery code at this installation's secure address.";
        return;
      }
      button.disabled = true;
      status.textContent = "Checking your recovery code…";
      try {
        const result = await request("/auth/passkeys/recover", { code: input.value });
        if (!/^[A-Za-z0-9_-]{43}$/.test(result.setup_token || "")) throw new Error("Recovery was not confirmed");
        setup = result.setup_token;
        input.value = "";
        document.getElementById("passkey-code-label").hidden = true;
        recovering = false;
        registering = true;
        button.textContent = "Create replacement passkey";
        status.textContent = "Your old sign-ins and passkeys are revoked. Create a replacement passkey to sign in.";
      } catch {
        status.textContent = "Recovery was not confirmed. If the code was already used, use another saved code or contact your administrator.";
      }
      button.disabled = false;
      return;
    }
    if (registering && !setup && setupInput) setup = setupInput.value.trim();
    if (!window.isSecureContext || !navigator.credentials || (registering && !/^[A-Za-z0-9_-]{43}$/.test(setup || ""))) {
      if (registering) setup = "";
      status.textContent = registering ? "Enter a valid setup code or use a fresh setup link." : "Open this installation's secure address to use a passkey.";
      return;
    }
    button.disabled = true;
    status.textContent = registering ? "Confirm creating your passkey." : "Confirm using your passkey.";
    const path = registering ? "/auth/passkeys/register" : "/auth/passkeys/login";
    try {
      const options = await request(path + "/options", registering ? { setup_token: setup } : {});
      if (registering && setupInput) setupInput.value = "";
      if (registering && setupLabel) setupLabel.hidden = true;
      const credential = registering ? await navigator.credentials.create(optionsFromJSON(options)) : await navigator.credentials.get(optionsFromJSON(options));
      if (!credential) throw new Error("Passkey was not used");
      const result = await request(path + "/verify", { credential: credentialJSON(credential) });
      const review = typeof result.continue === "string" && /^\/auth\/approve\?operation=([A-Za-z0-9_-]{43})$/.exec(result.continue);
      const exactReview = review && encode(decode(review[1])) === review[1];
      const local = result.continue === "/auth/recovery-codes" || /^\/auth\/agent\?request=[A-Za-z0-9_-]{43}$/.test(result.continue);
      if (result.signed_in !== true || (result.continue !== "/" && !/^\/oauth\/authorize\?request=[A-Za-z0-9_-]{43}$/.test(result.continue) && !exactReview && !local)) throw new Error("Sign-in was not confirmed");
      setup = "";
      try { sessionStorage.removeItem(setupKey); } catch { /* Nothing kept. */ }
      status.textContent = "Signed in. Continuing…";
      location.replace(result.continue);
    } catch {
      if (registering && setupInput && setupLabel) {
        setupInput.value = setup;
        setupLabel.hidden = false;
        setup = "";
      }
      status.textContent = registering ? "Passkey setup was not confirmed. Try signing in with your passkey, or request a fresh setup code." : "Sign-in was not confirmed. Try your passkey again.";
      button.disabled = false;
    }
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
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (busy) return;
    const code = input.value.replace(/\s+/g, "");
    if (!/^[0-9]{6}$/.test(code)) {
      status.textContent = "Enter the 6-digit code from your authenticator app.";
      return;
    }
    busy = true;
    status.textContent = "Checking your code…";
    try {
      const response = await fetch("/auth/authenticator/login", {
        method: "POST", credentials: "same-origin", redirect: "error", cache: "no-store",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
        body: JSON.stringify({ code }),
      });
      const json = response.headers.get("Content-Type")?.includes("application/json") ? await response.json() : {};
      if (!response.ok) {
        input.value = "";
        status.textContent = json.error === "invalid_code" ? "That code didn't work. Enter the newest code from your authenticator app."
          : json.error === "too_many_attempts" ? "Too many codes were tried. Wait five minutes, then try again."
          : "Sign-in was not confirmed. Reload this page and try again.";
        busy = false;
        return;
      }
      const operation = typeof json.continue === "string" && /^\/auth\/approve\?operation=([A-Za-z0-9_-]{43})$/.exec(json.continue);
      const bytes = operation && Uint8Array.from(atob(operation[1].replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
      const review = operation && btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "") === operation[1];
      const local = json.continue === "/" || /^\/auth\/agent\?request=[A-Za-z0-9_-]{43}$/.test(json.continue);
      if (json.signed_in !== true || (!review && !local)) throw new Error("Sign-in was not confirmed");
      status.textContent = "Signed in. Continuing…";
      location.replace(json.continue);
    } catch {
      status.textContent = "Sign-in was not confirmed. Reload this page and try again.";
      busy = false;
    }
  });
})();

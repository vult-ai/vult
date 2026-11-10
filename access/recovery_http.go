package access

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// LocalRecovery hands a new local installation's backup key to the recovery
// page once, beside the owner's new recovery codes. Neither passes through the
// terminal, where an agent could read it.
type LocalRecovery interface {
	TakeBackupKey(ctx context.Context, session string) (string, error)
}

var recoveryPage = template.Must(template.New("recovery").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="vult-login-csrf" content="{{.CSRF}}"><title>Save your recovery codes · Vult</title>` + authHead + `<script src="/auth/particles.js" defer></script><script src="/auth/recovery.js" defer></script></head><body>` + authBar + `<main class="recovery">
<section id="codes-step" aria-labelledby="codes-title">
<h1 id="codes-title">Save your recovery codes</h1>
<p>If you ever lose {{.Lost}}, these codes are the only way back into your Vult.</p>
<ul class="rules"><li>Each code works once.</li><li>Keep them in a password manager or another safe place.</li><li>Never share them. Vult will never ask for them.</li></ul>
<div id="codes-notice" class="notice-slot" aria-live="polite"><div></div></div>
<div class="code-actions"><button class="secondary" id="recovery-copy" type="button" hidden>` + copyIcon("when-ready") + checkMarkIcon("when-copied") + `<span>Copy codes</span></button><a class="button secondary" id="recovery-download" download="vult-recovery-codes.txt" href="{{.Download}}">` + downloadIcon() + `Download</a></div>
<ol class="codes" id="recovery-codes" aria-label="Recovery codes">{{range .Codes}}<li><code>{{.}}</code></li>{{end}}</ol>
{{if .BackupKey}}<h2>Backup key</h2><p class="fine start">Unlocks your encrypted backups.</p><p class="key"><code id="backup-key">{{.BackupKey}}</code></p>{{end}}
<p class="saved-note">You won’t see these codes again after this page.</p>
<button id="recovery-continue" type="button" disabled aria-describedby="recovery-hint">I’ve saved my codes</button>
<p class="gate-hint" id="recovery-hint">Copy or download your codes to continue, or <button type="button" id="recovery-written">I wrote them down</button>.</p>
</section>
<section id="protected-step" aria-labelledby="protected-title" hidden>
<h1 id="protected-title">Your account is protected</h1>
<p id="protected-lede">{{if and .HasPasskey .HasAuthenticator}}You sign in with your passkey or authenticator app. If you ever lose them, a recovery code gets you back in.{{else if .HasPasskey}}You sign in with your passkey. If you ever lose it, a recovery code gets you back in.{{else}}You sign in with your authenticator app. If you ever lose it, a recovery code gets you back in.{{end}}</p>
<div id="protected-notice" class="notice-slot" aria-live="polite"><div></div></div>
<ul class="summary" aria-label="How your account is protected"><li data-state="{{if .HasPasskey}}on{{else}}off{{end}}"><span>Passkey</span><span class="state">{{if .HasPasskey}}Set up{{else}}Not set up{{end}}</span></li><li id="summary-authenticator" data-state="{{if .HasAuthenticator}}on{{else}}off{{end}}"><span>Authenticator app</span><span class="state">{{if .HasAuthenticator}}Set up{{else}}Not set up{{end}}</span></li><li data-state="on"><span>Recovery codes</span><span class="state">Saved</span></li></ul>
{{if .OfferAuthenticator}}<div class="backup" id="backup-offer"><h2>Add an authenticator app as a backup</h2><p>Get a sign-in code on your phone if this device isn’t with you.</p><button class="link" type="button" id="backup-start">Add an authenticator app →</button></div>{{end}}
<button id="protected-continue" type="button">{{if .OfferAuthenticator}}Continue without a backup{{else}}Continue{{end}}</button>
</section>
{{if .OfferAuthenticator}}<section id="authenticator-setup" class="enroll" aria-labelledby="authenticator-title" hidden><h1 id="authenticator-title">Set up your authenticator</h1><ol class="steps"><li>Open your authenticator app.</li><li>Scan the QR code or enter the setup key.</li><li>Enter the 6-digit code to finish.</li></ol><div class="enroll-panel"><div class="manual"><code id="authenticator-secret" aria-label="Setup key"></code></div><div id="authenticator-qr" class="qr"></div></div><div id="authenticator-status" class="notice-slot" aria-live="polite"><div></div></div><div class="otp-heading"><label for="authenticator-code">Authenticator code</label></div><div class="otp" id="authenticator-otp"><input id="authenticator-code" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" minlength="6" maxlength="6" spellcheck="false"><div class="slots" id="authenticator-slots" aria-hidden="true"><span class="slot"></span><span class="slot"></span><span class="slot" data-separator></span><span class="slot"></span><span class="slot"></span><span class="slot"></span></div></div><button id="authenticator-verify" type="button" disabled>Verify and continue</button><div class="links"><button class="link end" id="authenticator-cancel" type="button">Cancel</button></div></section>{{end}}
</main></body></html>`))

// The codes are kept before setup continues: copying, downloading or writing
// them down unlocks "I've saved my codes". Then a summary shows what protects
// the account, offers an authenticator as a backup, and opens the Books.
const recoveryScript = `"use strict";
(() => {
  const say = (slot, feedback) => window.vultNotice?.(slot, feedback);
  const $ = (id) => document.getElementById(id);
  const copy = $("recovery-copy");
  const saved = $("recovery-continue");
  const hint = $("recovery-hint");
  const keep = () => {
    if (saved) saved.disabled = false;
    if (hint) hint.hidden = true;
  };
  const text = () => {
    const codes = Array.from(document.querySelectorAll("#recovery-codes code"), (c) => c.textContent).join("\n");
    const key = $("backup-key")?.textContent;
    return "Vult recovery codes\n\n" + codes + (key ? "\n\nBackup key\n" + key : "") + "\n";
  };
  if (copy && navigator.clipboard) {
    const label = copy.querySelector("span");
    let timer = 0;
    copy.hidden = false;
    // Copying confirms on the button itself, where the owner is looking.
    copy.addEventListener("click", () => navigator.clipboard.writeText(text()).then(() => {
      copy.dataset.copied = "";
      if (label) label.textContent = "Copied";
      say($("codes-notice"), null);
      keep();
      clearTimeout(timer);
      timer = setTimeout(() => { delete copy.dataset.copied; if (label) label.textContent = "Copy codes"; }, 2000);
    }, () => say($("codes-notice"), { title: "Couldn’t copy", detail: "Download your codes instead." })));
  }
  $("recovery-download")?.addEventListener("click", keep);
  $("recovery-written")?.addEventListener("click", keep);
  const show = (id) => {
    for (const section of ["codes-step", "protected-step", "authenticator-setup"]) {
      const node = $(section);
      if (node) node.hidden = section !== id;
    }
    window.scrollTo?.(0, 0);
    $(id)?.querySelector("h1")?.focus?.();
  };
  saved?.addEventListener("click", () => { if (!saved.disabled) show("protected-step"); });
  $("protected-continue")?.addEventListener("click", () => window.vultOpen?.("/"));

  // An authenticator as a backup, right after setup while the sign-in is fresh.
  const csrf = document.querySelector('meta[name="vult-login-csrf"]')?.content;
  const code = $("authenticator-code");
  const verify = $("authenticator-verify");
  const status = $("authenticator-status");
  const post = async (path, body) => {
    const response = await fetch(path, {
      method: "POST", credentials: "same-origin", redirect: "error", cache: "no-store",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf || "" },
      body: JSON.stringify(body),
    });
    const json = response.headers.get("Content-Type")?.includes("application/json") ? await response.json() : {};
    return { ok: response.ok, json };
  };
  $("backup-start")?.addEventListener("click", async (event) => {
    const start = event.currentTarget;
    start.disabled = true;
    say($("protected-notice"), null);
    try {
      const { ok, json } = await post("/auth/authenticator/enroll/options", {});
      if (!ok || typeof json.secret !== "string" || !/^[A-Z2-7]+$/.test(json.secret)) throw new Error("No key");
      $("authenticator-secret").textContent = json.secret;
      if (typeof json.qr === "string" && typeof DOMParser === "function") {
        const svg = new DOMParser().parseFromString(json.qr, "image/svg+xml").documentElement;
        if (svg && svg.nodeName === "svg") $("authenticator-qr").replaceChildren(svg);
      }
      show("authenticator-setup");
      code?.focus?.();
    } catch {
      say($("protected-notice"), { title: "Couldn’t start authenticator setup", detail: "Sign in again, then add it from your account.", tone: "warning" });
    }
    start.disabled = false;
  });
  $("authenticator-cancel")?.addEventListener("click", () => {
    if (code) code.value = "";
    say(status, null);
    show("protected-step");
  });
  const digits = () => (code?.value || "").replace(/\D/g, "").slice(0, 6);
  // Six cells over one real input, as on every Vult code entry.
  const slots = Array.from($("authenticator-slots")?.children || []);
  const otp = $("authenticator-otp");
  const paint = () => {
    const value = digits();
    const active = document.activeElement === code ? Math.min(value.length, 5) : -1;
    slots.forEach((slot, i) => {
      slot.textContent = value[i] || "";
      if (i === active) slot.setAttribute("data-active", "");
      else slot.removeAttribute("data-active");
    });
  };
  code?.addEventListener("focus", paint);
  code?.addEventListener("blur", paint);
  let checking = false;
  const confirm = async () => {
    if (checking || digits().length !== 6) return;
    checking = true;
    if (verify) { verify.disabled = true; verify.textContent = "Verifying…"; }
    say(status, null);
    try {
      const { ok, json } = await post("/auth/authenticator/enroll/verify", { code: digits() });
      if (!ok) {
        if (code) code.value = "";
        otp?.setAttribute("data-invalid", "");
        paint();
        say(status, json.error === "invalid_code"
          ? { title: "Code not accepted", detail: "Enter the latest code from your authenticator app. If it keeps failing, check that your phone sets its time automatically." }
          : json.error === "too_many_attempts" ? { title: "Too many attempts", detail: "Wait five minutes, then try again.", tone: "warning" }
          : { title: "Authenticator wasn’t added", detail: "Sign in again, then add it from your account." });
      } else {
        // Back to the summary, now with the authenticator ticked.
        const row = $("summary-authenticator");
        if (row) { row.dataset.state = "on"; row.querySelector(".state").textContent = "Set up"; }
        $("backup-offer")?.remove();
        const lede = $("protected-lede");
        if (lede) lede.textContent = "You sign in with your passkey or authenticator app. If you ever lose them, a recovery code gets you back in.";
        const next = $("protected-continue");
        if (next) next.textContent = "Continue";
        show("protected-step");
        say($("protected-notice"), { title: "Authenticator added", tone: "success" });
      }
    } catch {
      say(status, { title: "Authenticator wasn’t added", detail: "Check your connection and try again." });
    }
    checking = false;
    if (verify) { verify.textContent = "Verify and continue"; verify.disabled = digits().length !== 6; }
  };
  code?.addEventListener("input", () => {
    if (code.value !== digits()) code.value = digits();
    if (verify) verify.disabled = checking || digits().length !== 6;
    // Editing a rejected code clears its message.
    if (otp?.hasAttribute("data-invalid")) say(status, null);
    otp?.removeAttribute("data-invalid");
    paint();
    if (digits().length === 6) confirm();
  });
  verify?.addEventListener("click", confirm);
})();
`

func (h *HTTP) localRecovery() LocalRecovery {
	if r, ok := h.agents.(LocalRecovery); ok && h.local {
		return r
	}
	return nil
}

func (h *HTTP) recoveryRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	// Every installation shows the owner their recovery codes here, once, right
	// after they set up sign-in. Whoever ran setup never receives them.
	if h.passkeys == nil || r.Method != http.MethodGet || r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		fail(w, http.StatusNotFound)
		return
	}
	session, fresh := h.freshSession(r)
	if !fresh {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	codes, err := h.passkeys.IssueRecoveryCodes(r.Context(), session)
	if err != nil || len(codes) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	key := ""
	if recovery := h.localRecovery(); recovery != nil {
		key, _ = recovery.TakeBackupKey(r.Context(), session)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	download := "Vult recovery codes\n\n" + strings.Join(codes, "\n")
	if key != "" {
		download += "\n\nBackup key\n" + key
	}
	hasPasskey := h.passkeys != nil && h.passkeys.HasPasskey(r.Context())
	hasAuthenticator := h.totp != nil && h.totp.Ready(r.Context())
	lost := "your authenticator"
	if hasPasskey && hasAuthenticator {
		lost = "your sign-in methods"
	} else if hasPasskey {
		lost = "your passkey"
	}
	// The page also adds a backup authenticator, so it needs the sign-in nonce.
	nonce := Random()
	cookie(w, loginCookie, nonce, 600)
	_ = recoveryPage.Execute(w, struct {
		CSRF               string
		Codes              []string
		BackupKey          string
		Download           template.URL
		Lost               string
		HasPasskey         bool
		HasAuthenticator   bool
		OfferAuthenticator bool
	}{nonce, codes, key, template.URL("data:text/plain;charset=utf-8," + url.PathEscape(download+"\n")), lost, hasPasskey, hasAuthenticator,
		hasPasskey && !hasAuthenticator && h.totp != nil})
}

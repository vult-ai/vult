package access

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// LocalRecovery shows the setup secrets of a new local installation once, in
// the browser, right after the owner creates their passkey. They never pass
// through the terminal, where an agent could read them.
type LocalRecovery interface {
	TakeRecovery(ctx context.Context, session string) (codes []string, backupKey string, err error)
}

var recoveryPage = template.Must(template.New("recovery").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Save your recovery codes · Vult</title>` + authHead + `<script src="/auth/particles.js" defer></script><script src="/auth/recovery.js" defer></script></head><body>` + authBar + `<main class="recovery">
<h1>If you can’t sign in, these let you back in</h1>
<p>Each code works once. Keep them somewhere safe, like a password manager. Never give them to an agent.</p>
<div class="code-actions"><button class="secondary" id="recovery-copy" type="button" hidden>` + copyIcon("when-ready") + checkMarkIcon("when-copied") + `<span>Copy codes</span></button><a class="button secondary" id="recovery-download" download="vult-recovery-codes.txt" href="{{.Download}}">` + downloadIcon() + `Download</a></div>
<ol class="codes" id="recovery-codes" aria-label="Recovery codes">{{range .Codes}}<li><code>{{.}}</code></li>{{end}}</ol>
{{if .BackupKey}}<h2>Backup key</h2><p class="fine start">Unlocks your encrypted backups.</p><p class="key"><code id="backup-key">{{.BackupKey}}</code></p>{{end}}
<p class="saved-note">You won’t see these codes again.</p>
<a class="button" id="recovery-continue" href="/">I’ve saved my codes</a>
<p id="recovery-status" class="sr-only" role="status" aria-live="polite"></p>
</main></body></html>`))

// The page's copy button confirms on itself; Download saves a text file.
const recoveryScript = `"use strict";
(() => {
  const copy = document.getElementById("recovery-copy");
  const status = document.getElementById("recovery-status");
  const text = () => {
    const codes = Array.from(document.querySelectorAll("#recovery-codes code"), (c) => c.textContent).join("\n");
    const key = document.getElementById("backup-key")?.textContent;
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
      status.textContent = "Recovery codes copied.";
      clearTimeout(timer);
      timer = setTimeout(() => { delete copy.dataset.copied; if (label) label.textContent = "Copy codes"; }, 2000);
    }, () => { status.textContent = "Couldn’t copy. Use Download instead."; }));
  }
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
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	recovery := h.localRecovery()
	if recovery == nil || r.Method != http.MethodGet || r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		fail(w, http.StatusNotFound)
		return
	}
	session, fresh := h.freshSession(r)
	if !fresh {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	codes, key, err := recovery.TakeRecovery(r.Context(), session)
	if err != nil || len(codes) == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	download := "Vult recovery codes\n\n" + strings.Join(codes, "\n")
	if key != "" {
		download += "\n\nBackup key\n" + key
	}
	_ = recoveryPage.Execute(w, struct {
		Codes     []string
		BackupKey string
		Download  template.URL
	}{codes, key, template.URL("data:text/plain;charset=utf-8," + url.PathEscape(download+"\n"))})
}

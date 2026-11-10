"use strict";

// Shared by Vult's sign-in pages: one way to show a message, and one way to
// leave for the Books once sign-in or setup is done.
(() => {
  const svg = (path) =>
    '<svg class="notice-icon" viewBox="0 0 24 24" aria-hidden="true"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="' + path + '"/></svg>';
  const icons = {
    error: svg("M12 8v4m0 4h.01M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z"),
    warning: svg("M12 9v4m0 4h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z"),
    neutral: svg("M12 16v-4m0-4h.01M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z"),
    success: svg("m7.5 12 3 3 6-6M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z"),
  };

  // A message: a short title and, when useful, the next step. Errors and
  // warnings are alerts; everything else is status. The slot opens and closes
  // in place, so the page doesn't jump.
  window.vultNotice = (slot, feedback) => {
    if (!slot) return;
    if (!feedback) {
      slot.removeAttribute("data-open");
      return;
    }
    const tone = feedback.tone || "error";
    const body = document.createElement("div");
    body.className = "notice";
    body.dataset.tone = tone;
    body.setAttribute("role", tone === "error" || tone === "warning" ? "alert" : "status");
    body.innerHTML = icons[tone] || icons.error;
    const text = document.createElement("div");
    const title = document.createElement("p");
    title.className = "notice-title";
    title.textContent = feedback.title;
    text.append(title);
    if (feedback.detail) {
      const detail = document.createElement("p");
      detail.textContent = feedback.detail;
      text.append(detail);
    }
    body.append(text);
    let inner = slot.firstElementChild;
    if (!inner) {
      inner = document.createElement("div");
      slot.append(inner);
    }
    inner.replaceChildren(body);
    requestAnimationFrame(() => slot.setAttribute("data-open", ""));
  };

  // Sign-in and setup are done: a calm moment, then the Books.
  window.vultOpen = (destination) => {
    const main = document.querySelector("main");
    if (main) {
      const section = document.createElement("section");
      section.className = "opening";
      section.setAttribute("aria-busy", "true");
      section.innerHTML = '<span class="spinner" aria-hidden="true"></span><h1>Opening Vult</h1>';
      main.replaceChildren(section);
    }
    location.replace(destination);
  };

  // Where sign-in may continue: the Books, a page the owner left, or one of
  // the sign-in steps that follow. Never another site.
  window.vultContinuation = (value) => {
    if (typeof value !== "string" || value.length > 1024 || !value.startsWith("/") || value.startsWith("//") || /[\\#\s]/.test(value)) return false;
    if (/^\/auth\/recovery-codes$/.test(value) || /^\/auth\/agent\?request=[A-Za-z0-9_-]{43}$/.test(value)) return true;
    if (/^\/oauth\/authorize\?request=[A-Za-z0-9_-]{43}$/.test(value)) return true;
    const review = /^\/auth\/approve\?operation=([A-Za-z0-9_-]{43})$/.exec(value);
    if (review) {
      // Only the exact encoding the server issues.
      try {
        const bytes = Uint8Array.from(atob(review[1].replace(/-/g, "+").replace(/_/g, "/")), (c) => c.charCodeAt(0));
        return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "") === review[1];
      } catch {
        return false;
      }
    }
    const path = value.split("?")[0].toLowerCase();
    if (path.split("/").some((part) => part === "." || part === "..")) return false;
    return !/^\/(auth|identity|api|mcp|oauth|\.well-known)(\/|$)/.test(path);
  };
})();

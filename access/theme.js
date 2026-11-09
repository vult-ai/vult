// Applies the owner's Vult theme before the first paint, with the same stored
// preference as the app, and runs the theme menu in the sign-in top bar.
(() => {
  "use strict";
  const key = "vult-theme";
  const valid = (value) => value === "system" || value === "light" || value === "dark";
  const root = document.documentElement;
  const system = window.matchMedia("(prefers-color-scheme: dark)");
  const labels = { light: "Light", dark: "Dark", system: "System" };
  let preference = "system";
  try {
    const stored = localStorage.getItem(key);
    const shared = (document.cookie.match(/(?:^|;\s*)vult-theme=([a-z]+)/) || [])[1];
    if (valid(stored)) preference = stored;
    else if (valid(shared)) preference = shared;
  } catch {
    // Storage can be unavailable; the system theme still works.
  }
  const resolved = () => (preference === "system" ? (system.matches ? "dark" : "light") : preference);
  const apply = () => {
    root.classList.remove("light-mode", "dark-mode");
    root.classList.add(resolved() + "-mode");
    root.dataset.vultThemePreference = preference;
  };
  apply();
  system.addEventListener("change", () => {
    if (preference === "system") apply();
  });

  function choose(next) {
    preference = next;
    apply();
    try {
      localStorage.setItem(key, next);
    } catch {
      // The choice still applies to this page.
    }
    const attributes = "; Path=/; Max-Age=31536000; SameSite=Lax" + (location.protocol === "https:" ? "; Secure" : "");
    document.cookie = key + "=" + next + attributes;
    document.cookie = key + "-resolved=" + resolved() + attributes;
  }

  function wire() {
    const control = document.getElementById("theme");
    const button = document.getElementById("theme-button");
    const menu = document.getElementById("theme-menu");
    if (!control || !button || !menu) return;
    const items = Array.from(menu.querySelectorAll("[data-theme-choice]"));
    const sync = () => {
      button.setAttribute("aria-label", "Theme: " + labels[preference]);
      for (const item of items) item.setAttribute("aria-checked", String(item.dataset.themeChoice === preference));
    };
    const open = () => {
      menu.hidden = false;
      button.setAttribute("aria-expanded", "true");
      (items.find((item) => item.dataset.themeChoice === preference) || items[0]).focus();
    };
    const close = (focus) => {
      if (menu.hidden) return;
      menu.hidden = true;
      button.setAttribute("aria-expanded", "false");
      if (focus) button.focus();
    };
    button.addEventListener("click", () => (menu.hidden ? open() : close(true)));
    button.addEventListener("keydown", (event) => {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        open();
      }
    });
    for (const item of items) {
      item.addEventListener("click", () => {
        choose(item.dataset.themeChoice);
        sync();
        close(true);
      });
    }
    menu.addEventListener("keydown", (event) => {
      const at = items.indexOf(document.activeElement);
      if (event.key === "Escape") close(true);
      else if (event.key === "Tab") close(false);
      else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : items.length - 1;
        items[(at + step) % items.length].focus();
      } else if (event.key === "Home" || event.key === "End") {
        event.preventDefault();
        items[event.key === "Home" ? 0 : items.length - 1].focus();
      }
    });
    document.addEventListener("click", (event) => {
      if (!control.contains(event.target)) close(false);
    });
    sync();
    control.hidden = false;
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", wire);
  else wire();
})();

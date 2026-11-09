// Runs before the first paint, under the Go server's self-only script policy.
(() => {
  let preference = "system";
  const valid = (value) => ["system", "light", "dark"].includes(value);
  try {
    const stored = localStorage.getItem("vult-theme");
    if (valid(stored)) preference = stored;
    else {
      // A first visit keeps the theme chosen on the site that opened this Vult.
      const shared = document.cookie.match(/(?:^|;\s*)vult-theme=([a-z]+)/)?.[1];
      if (valid(shared)) {
        preference = shared;
        localStorage.setItem("vult-theme", shared);
      }
    }
  } catch {
    // Storage can be unavailable; the system theme still works.
  }
  const theme =
    preference === "system"
      ? matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light"
      : preference;
  document.documentElement.classList.remove("light-mode", "dark-mode");
  document.documentElement.classList.add(theme + "-mode");
  document.documentElement.dataset.vultThemePreference = preference;
  document.documentElement.style.colorScheme = theme;
})();

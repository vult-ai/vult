// Runs before the first paint, under the Go server's self-only script policy.
(() => {
  let preference = "system";
  try {
    const stored = localStorage.getItem("vult-theme");
    if (["system", "light", "dark"].includes(stored)) preference = stored;
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

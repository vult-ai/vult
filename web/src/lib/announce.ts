/**
 * One polite live region for the whole interface. Regions mounted together
 * with their text are often not read, and several regions repeat each other;
 * notices speak through this one instead.
 */
let region: HTMLElement | null = null;
let pending = 0;

function liveRegion() {
  if (region?.isConnected) return region;
  region = document.createElement("div");
  region.setAttribute("role", "status");
  region.setAttribute("aria-live", "polite");
  region.setAttribute("aria-atomic", "true");
  region.dataset.vultAnnouncer = "";
  region.className = "sr-only";
  document.body.append(region);
  return region;
}

export function announce(message: string) {
  if (typeof document === "undefined" || !message.trim()) return;
  const target = liveRegion();
  const turn = ++pending;
  // Clear first so repeating the same sentence is announced again.
  target.textContent = "";
  window.setTimeout(() => {
    if (turn === pending) target.textContent = message;
  }, 60);
}

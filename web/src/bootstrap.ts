import { showStartupError } from "./components/ui/startup-error";
import { readInitialBooks } from "./lib/book-state";
import { startApplication } from "./lib/startup";
import "./style.css";

// After Vult is updated, a page opened before the update asks for files that
// no longer exist. Reload once to pick up the new version instead of showing
// a blank page.
const reloadKey = "vult.reloaded-for-update";
/** Reloads once per page for a missing file; a second failure stays visible. */
function reloadForUpdate() {
  try {
    if (window.sessionStorage.getItem(reloadKey) === location.pathname) return false;
    window.sessionStorage.setItem(reloadKey, location.pathname);
  } catch {
    return false;
  }
  window.location.reload();
  return true;
}
// Chrome, Safari and Firefox word a missing module file differently.
const missingModule = /Failed to fetch dynamically imported module|Importing a module script failed|error loading dynamically imported module|Unable to preload CSS/i;
const isMissingModule = (reason: unknown) =>
  missingModule.test(reason instanceof Error ? reason.message : String(reason ?? ""));
window.addEventListener("vite:preloadError", (event) => {
  if (reloadForUpdate()) event.preventDefault();
});
// A lazily loaded screen whose file is gone surfaces as an uncaught error.
// After the one reload, a file that is still missing shows the startup
// failure screen, which works without the application's own files.
function recover(reason: unknown) {
  if (!isMissingModule(reason) || reloadForUpdate()) return;
  // After the interface has finished unmounting, so the notice stays.
  window.setTimeout(() =>
    showStartupError(document.getElementById("root")!, reason, {
      title: "Vult was updated",
      detail: "Reload to open the new version.",
    }),
  );
}
window.addEventListener("error", (event) => recover(event.error ?? event.message));
window.addEventListener("unhandledrejection", (event) => recover(event.reason));
window.setTimeout(() => {
  try {
    window.sessionStorage.removeItem(reloadKey);
  } catch {
    /* Nothing kept. */
  }
}, 10_000);

const startup = startApplication({
  read: readInitialBooks,
  load: () => import("./main"),
});
void startup.catch((error: unknown) => {
  showStartupError(document.getElementById("root")!, error);
});

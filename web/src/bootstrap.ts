import { showStartupError } from "./components/ui/startup-error";
import { readInitialBooks } from "./lib/book-state";
import { startApplication } from "./lib/startup";
import "./style.css";

// After Vult is updated, a page opened before the update asks for files that
// no longer exist. Reload once to pick up the new version instead of showing
// a blank page.
const reloadKey = "vult.reloaded-for-update";
window.addEventListener("vite:preloadError", (event) => {
  try {
    if (window.sessionStorage.getItem(reloadKey) === location.pathname) return;
    window.sessionStorage.setItem(reloadKey, location.pathname);
  } catch {
    return;
  }
  event.preventDefault();
  window.location.reload();
});
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

import { useSyncExternalStore } from "react";

/* Whether the Dashboard hides its amounts. Shown unless the viewer hides them;
   the choice is remembered in this browser only, so a browser that chose to
   hide keeps its figures covered on a shared screen or a call. Charts keep
   their shapes. */
const key = "vult:amounts-hidden";
const listeners = new Set<() => void>();
let hidden: boolean | undefined;

function read() {
  try {
    return localStorage.getItem(key) === "hidden";
  } catch {
    return false;
  }
}

export function setAmountsHidden(value: boolean) {
  hidden = value;
  try {
    localStorage.setItem(key, value ? "hidden" : "shown");
  } catch {
    /* Without storage the choice lasts until the page reloads. */
  }
  for (const listener of listeners) listener();
}

export function useAmountsHidden() {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => (hidden ??= read()),
    () => false,
  );
}

/** What stands in for an amount while amounts are hidden. */
export const hiddenAmount = "••••••";
export const hiddenCompact = "•••";

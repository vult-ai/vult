import { useSyncExternalStore } from "react";

/* Whether the Dashboard hides its amounts. Hidden until the viewer shows them,
   remembered in this browser only, so a Book opened on a shared screen or a
   call starts with its figures covered. Charts keep their shapes. */
const key = "vult:amounts-hidden";
const listeners = new Set<() => void>();
let hidden: boolean | undefined;

function read() {
  try {
    return localStorage.getItem(key) !== "shown";
  } catch {
    return true;
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
    () => true,
  );
}

/** What stands in for an amount while amounts are hidden. */
export const hiddenAmount = "••••••";
export const hiddenCompact = "•••";

import type { UserPreferences } from "./user-preferences";

/* The viewer's regional format, date style and negative style, for formatters that
   are called outside React (or by components that don't read preferences). The
   preference providers set it while rendering, before the pages below them render,
   so every amount and date on every page follows the same choice. */
let current: Readonly<UserPreferences> | undefined;
export const displayPreferences = () => current;
export function setDisplayPreferences(value: Readonly<UserPreferences> | undefined) {
  current = value;
}

export type AccountPresentation = { alias: string; institutionId: string };
export type BookPresentation = {
  version: 1;
  accounts: Record<string, AccountPresentation>;
  order: string[];
};
export function emptyAccountPresentation(): BookPresentation {
  return { version: 1, accounts: Object.create(null), order: [] };
}
export const accountPresentationKey = (bookId: string) =>
  `vult.account-presentation.v1:${bookId}`;
export function parseAccountPresentation(raw: string | null): BookPresentation {
  if (!raw) return emptyAccountPresentation();
  if (raw.length > 524_288)
    throw new Error("Display preferences are too large.");
  const value = JSON.parse(raw);
  // Preserve positions from the earlier preview without retaining pin behavior.
  const order = value?.order ?? value?.pins ?? [];
  if (
    value?.version !== 1 ||
    !value.accounts ||
    typeof value.accounts !== "object" ||
    Array.isArray(value.accounts) ||
    !Array.isArray(order) ||
    Object.keys(value.accounts).length > 2000 ||
    order.length > 2000
  )
    throw new Error("Display preferences are invalid.");
  const result = emptyAccountPresentation();
  const code = (value: unknown): value is string =>
    typeof value === "string" && value.length > 0 && value.length <= 512;
  for (const [key, entry] of Object.entries(value.accounts) as [
    string,
    AccountPresentation,
  ][]) {
    if (
      !code(key) ||
      !entry ||
      typeof entry.alias !== "string" ||
      entry.alias.length > 80 ||
      typeof entry.institutionId !== "string" ||
      (entry.institutionId !== "" &&
        !/^[a-z][a-z0-9-]{0,63}$/.test(entry.institutionId))
    )
      throw new Error("Display preferences are invalid.");
    result.accounts[key] = {
      alias: entry.alias.trim(),
      institutionId: entry.institutionId,
    };
  }
  if (
    order.some((id: unknown) => !code(id)) ||
    new Set(order).size !== order.length
  )
    throw new Error("Account order is invalid.");
  result.order = [...order];
  return result;
}

/** Move only visible slots; filtered-out accounts retain their exact positions. */
export function reorderVisibleAccounts(
  complete: readonly string[],
  visible: readonly string[],
  moved: string,
  destination: number,
) {
  const source = visible.indexOf(moved);
  if (source < 0 || !Number.isInteger(destination)) return [...complete];
  const next = [...visible];
  next.splice(source, 1);
  next.splice(Math.max(0, Math.min(next.length, destination)), 0, moved);
  const selected = new Set(visible);
  let index = 0;
  return complete.map((code) => (selected.has(code) ? next[index++] : code));
}
export function readAccountPresentation(bookId: string) {
  return parseAccountPresentation(
    localStorage.getItem(accountPresentationKey(bookId)),
  );
}
export function saveAccountPresentation(
  bookId: string,
  value: BookPresentation,
) {
  const raw = JSON.stringify(value);
  parseAccountPresentation(raw);
  localStorage.setItem(accountPresentationKey(bookId), raw);
  window.dispatchEvent(
    new CustomEvent("vult-account-presentation", { detail: bookId }),
  );
}

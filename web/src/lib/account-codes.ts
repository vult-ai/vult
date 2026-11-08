import { get, post } from "./api";

export type AccountCode = {
  account_reference: string;
  gl_code: string;
  revision: number;
  prior_gl_code: string;
  actor: string;
  recorded_at: string;
};
export type AccountCodeView = {
  account_reference: string;
  gl_code: string;
  revision: number;
  history: AccountCode[];
};
export const accountCodeQuery = (book: string, account: string) =>
  `account-codes?${new URLSearchParams({ book_id: book, account_reference: account })}`;
export const readAccountCode = (
  book: string,
  account: string,
  signal?: AbortSignal,
) => get<AccountCodeView>(accountCodeQuery(book, account), signal);
export async function saveAccountCode(
  book: string,
  account: string,
  glCode: string,
  revision: number,
  key: string,
) {
  const result = await post<AccountCode>(accountCodeQuery(book, account), {
    book_id: book,
    account_reference: account,
    gl_code: glCode,
    revision,
    idempotency_key: key,
  });
  window.dispatchEvent(
    new CustomEvent("vult-account-code-changed", { detail: book }),
  );
  return result;
}
/** Assigned human GL codes are always visible; absent codes stay absent. */
export function accountCodeLabel(name: string, code?: string) {
  return code ? `${code} · ${name}` : name;
}

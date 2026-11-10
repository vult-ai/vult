import { fetchJSON, fetchResponse, readResponse } from "./errors";
import { writeHeaders } from "./session";

/** One unit of money an account still holds, in exact minor units. */
export type RemainingBalance = { currency: string; scale: number; amount_minor: string };

/** A retirement refused because the account still holds money. The owner says
 * where it went and Vult records the transfer with the retirement. */
export class BalanceRemaining extends Error {
  constructor(
    message: string,
    readonly remaining: RemainingBalance[],
  ) {
    super(message);
    this.name = "BalanceRemaining";
  }
}

export type AccountChange =
  | {
      operation: "revise_account";
      arguments: {
        request_key: string;
        account_id: string;
        expected_revision: number;
        code: string;
        name: string;
        /** Omit to keep the current parent; an empty string clears it. */
        parent_account_id?: string;
        /** Omit to keep the current setting. */
        heading?: boolean;
      };
    }
  | {
      operation: "retire_account";
      arguments: {
        request_key: string;
        account_id: string;
        expected_revision: number;
        destination_account_id?: string;
        transfer_date?: string;
        description?: string;
      };
    }
  | {
      operation: "reactivate_account";
      arguments: { request_key: string; account_id: string; expected_revision: number };
    }
  | { operation: "apply_starter_chart"; arguments: { request_key: string; template: string } }
  | {
      operation: "create_payee";
      arguments: { request_key: string; display_name: string; kind: PayeeKind };
    }
  | {
      operation: "revise_payee";
      arguments: {
        request_key: string;
        payee_id: string;
        expected_revision: number;
        display_name: string;
        kind: PayeeKind;
        status: "active" | "retired";
        reason: string;
      };
    }
  | {
      operation: "set_entry_payee";
      arguments: {
        request_key: string;
        entry_id: string;
        expected_revision: number;
        payee_id?: string;
        unknown?: true;
        reason?: string;
      };
    };

export type StatementMatchPart = { row_id?: string; line_id?: string; amount_minor?: string };
/** Statement decisions the owner makes in Reconcile. */
export type StatementChange =
  | { operation: "match_statement"; arguments: { request_key: string; rows: StatementMatchPart[]; lines: StatementMatchPart[]; reason?: string } }
  | { operation: "revoke_statement_match"; arguments: { request_key: string; match_id: string; reason: string } }
  | { operation: "mark_statement_duplicate"; arguments: { request_key: string; row_id: string; canonical_row_id: string; reason: string } }
  | { operation: "record_statement_adjustment"; arguments: { request_key: string; row_id: string; entry_id: string } }
  | { operation: "review_statement"; arguments: { request_key: string; statement_id: string; expected_revision: number; outcome: "reconciled" | "needs_work"; reason?: string } };

/** A rule Vult applied to refuse a change, in words the owner can act on. */
export class ChangeRefused extends Error {}

export type PayeeKind = "person" | "organization";
/** A person or organization the Book deals with. Same-name payees stay separate. */
export type Payee = {
  id: string;
  revision: number;
  display_name: string;
  kind: PayeeKind;
  brand_id?: string;
  status: "active" | "retired";
  recorded_at: string;
  entries: number;
};

export const readPayees = (book: string, signal?: AbortSignal) =>
  fetchJSON<{ payees: Payee[] }>(`/api/v1/payees?${new URLSearchParams({ book_id: book })}`, signal ? { signal } : undefined).then((out) => out.payees);

/** Records one owner change to the chart of accounts. Retries with the same
 * request key return the original outcome. */
export async function changeAccounts(book: string, change: AccountChange | StatementChange): Promise<unknown> {
  const response = await fetchResponse("/api/v1/account-changes", {
    method: "POST",
    headers: await writeHeaders(),
    body: JSON.stringify({ book_id: book, ...change }),
  });
  if (response.status === 422) {
    const body = (await response.clone().json().catch(() => null)) as
      | { error?: string; message?: string; remaining?: RemainingBalance[] }
      | null;
    if (body?.error === "balance_remaining" && Array.isArray(body.remaining))
      throw new BalanceRemaining(body.message || "This account still holds money.", body.remaining);
    if (body?.error === "refused" && body.message) throw new ChangeRefused(body.message);
  }
  const result = await readResponse<unknown>(response, "write");
  window.dispatchEvent(new CustomEvent("vult-accounts-changed", { detail: book }));
  return result;
}

export type StarterChart = {
  id: string;
  profile: string;
  title: string;
  description: string;
  accounts: { code: string; name: string; class: string; role?: string; parent?: string }[];
};

export const readStarterCharts = (signal?: AbortSignal) =>
  fetchJSON<{ charts: StarterChart[] }>("/api/v1/starter-charts", signal ? { signal } : undefined);

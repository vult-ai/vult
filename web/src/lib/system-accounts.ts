/** Accounts the interface treats specially are found by their recorded role.
 * The managed chart gave them fixed codes instead; those codes identify them
 * only there. An account is never treated specially because of its name. */
export type SystemRole =
  | "operating_cash"
  | "sales"
  | "general_expense"
  | "fx_gain"
  | "fx_loss"
  | "owner_reimbursements_payable"
  | "owner_loans_payable";

const legacyCode: Record<SystemRole, string> = {
  operating_cash: "1000",
  sales: "4000",
  general_expense: "5000",
  fx_gain: "4100",
  fx_loss: "5100",
  owner_reimbursements_payable: "2100",
  owner_loans_payable: "2200",
};

type ChartAccount = { code: string; role?: string };

/** The account holding a role, or undefined when this Book records none. */
export function systemAccount(accounts: readonly ChartAccount[], role: SystemRole) {
  return (
    accounts.find((account) => account.role === role) ??
    // A chart that records roles is authoritative; legacy codes apply only
    // to a chart that records none.
    (accounts.some((account) => account.role)
      ? undefined
      : accounts.find((account) => account.code === legacyCode[role]))
  )?.code;
}

/** The identities holding any of these roles. */
export function systemAccounts(accounts: readonly ChartAccount[], roles: readonly SystemRole[]) {
  return new Set(roles.map((role) => systemAccount(accounts, role)).filter((code): code is string => !!code));
}

/** Valuation legs: realized exchange gains and losses. */
export const valuationRoles = ["fx_gain", "fx_loss"] as const;

import { errorCopy, failureStateCopy } from "./error-copy";
/** Normalize failures and select approved copy. Server prose is never UI copy. */
export type FailureIntent = "read" | "write" | "action";
export type FailureKind =
  | "connection"
  | "unavailable"
  | "unsupported"
  | "access"
  | "missing"
  | "validation"
  | "conflict"
  | "uncertain"
  | "unexpected";
export type FailureContext = {
  intent?: FailureIntent;
  resource?:
    | "dashboard"
    | "accounts"
    | "balances"
    | "openings"
    | "activity"
    | "document"
    | "sources"
    | "settings"
    | "statement"
    | "search"
    | "approvals"
    | "app";
  retained?: "view" | "form";
};
export type Failure = {
  kind: FailureKind;
  message: string;
  status?: number;
  code?: string;
  outcome: "rejected" | "unknown" | "read";
  /**
   * "sign-in" when this browser has no session; "confirm" when its session
   * ended and the owner only needs to confirm it's them.
   */
  recovery: "retry" | "sign-in" | "confirm" | "review" | "none";
};
export type FailureSeverity = "routine" | "attention" | "critical";
export type FailureIcon =
  "connection" | "file" | "access" | "attention" | "critical" | "generic";

// Only approved read validators may create this evidence. Response prose,
// transport status and caller-supplied severity are never critical authority.
const verifiedReads = new WeakSet<Error>();
export function verifiedReadFailure(
  reason: "source-digest" | "ledger-balance" | "book-scope" | "record-identity",
  diagnostic: string,
): Error {
  const error = new Error(diagnostic);
  if (
    [
      "source-digest",
      "ledger-balance",
      "book-scope",
      "record-identity",
    ].includes(reason)
  )
    verifiedReads.add(error);
  return error;
}
const copy = {
  connection: errorCopy.connection,
  unavailable: errorCopy.unavailable,
  unsupported: errorCopy.featureUnavailable,
  access: errorCopy.access,
  signIn: errorCopy.signIn,
  sessionEnded: errorCopy.sessionEnded,
  missing: errorCopy.missing,
  validation: errorCopy.validation,
  conflict: errorCopy.conflict,
  uncertain: errorCopy.uncertain,
  unexpected: errorCopy.unavailable,
} as const;

const reviewedValidation: Array<[RegExp, string]> = [
  [/^receipt must be between 1 byte and 10 MiB$/i, errorCopy.receiptSize],
  [
    /^receipt must be a PNG, JPEG, or PDF matching its declared type$/i,
    errorCopy.receiptType,
  ],
  [
    /^receipt filename must be a plain filename of at most 180 characters$/i,
    errorCopy.fileName,
  ],
  [/^(?:this (?:file|document) is empty|empty file)\.?$/i, errorCopy.fileEmpty],
  [
    /^receipt (?:image|PDF) is malformed$|^invalid PDF structure\.?$|^could not read this package\.?$/i,
    errorCopy.fileUnreadable,
  ],
  [
    /^same-currency (?:source and bank amounts|card settlements) must match; record identified fees separately$/i,
    errorCopy.sameCurrencyAmounts,
  ],
  [
    /^opening currency must match the selected account$/i,
    errorCopy.openingCurrency,
  ],
  [
    /^(?:preview and confirm the conversion before saving|selected conversion was not found; preview again|selected conversion does not match this currency pair and date)$/i,
    errorCopy.rateReview,
  ],
  [/another Book|different Book/i, errorCopy.bookMismatch],
  [/quote.*expir|rate.*expir/i, errorCopy.rateExpired],
  [/debits.*credits.*balance/i, errorCopy.entryUnbalanced],
  [
    /^enter a positive decimal amount without separators\.?$/i,
    errorCopy.amountFormat,
  ],
  [
    /amount.*(positive|greater than zero)|positive.*amount/i,
    errorCopy.amountPositive,
  ],
  [/receipt.*(large|size|10.*mi?b)/i, errorCopy.receiptSize],
  [/currency.*(match|differ)|different.*currenc/i, errorCopy.currencies],
  [/date.*(invalid|required)|invalid.*date/i, errorCopy.dateInvalid],
  [/insufficient.*(cash|balance)|negative.*cash/i, errorCopy.cashInsufficient],
];

export class APIError extends Error {
  readonly failure: Failure;
  constructor(
    message: string,
    public status: number,
    failure?: Failure,
  ) {
    // The two-argument constructor is retained for reviewed local validation.
    const local = safeLocalMessage(message);
    super(failure?.message ?? local);
    this.name = "APIError";
    this.failure = failure ?? {
      kind: status === 400 || status === 422 ? "validation" : "uncertain",
      message: local,
      status,
      outcome:
        status >= 400 &&
        status < 500 &&
        status !== 408 &&
        status !== 409 &&
        status !== 429
          ? "rejected"
          : "unknown",
      recovery: "review",
    };
  }
}

export function responseFailure(
  status: number,
  body: unknown,
  intent: FailureIntent = "read",
  malformed = false,
): APIError {
  const data =
    body && typeof body === "object" ? (body as Record<string, unknown>) : {};
  const raw = typeof data.error === "string" ? data.error : "";
  const code =
    typeof data.code === "string" && /^[A-Z][A-Z0-9_]{0,63}$/.test(data.code)
      ? data.code
      : undefined;
  const uncertain =
    intent === "write" &&
    (status === 0 || status === 408 || status >= 500 || malformed);
  let failure: Failure;
  if (uncertain)
    failure = {
      kind: "uncertain",
      message: copy.uncertain,
      outcome: "unknown",
      recovery: "review",
    };
  else if (intent === "read" && status === 501 && code === "FEATURE_UNAVAILABLE")
    failure = {
      kind: "unsupported",
      message: copy.unsupported,
      outcome: "read",
      recovery: "none",
    };
  else if (status === 401 && code === "SIGNED_OUT")
    failure = {
      kind: "access",
      message: copy.signIn,
      outcome: "rejected",
      recovery: "sign-in",
    };
  else if (status === 401)
    // A session this browser held ended; the account is still known.
    failure = {
      kind: "access",
      message: copy.sessionEnded,
      outcome: "rejected",
      recovery: "confirm",
    };
  else if (status === 403)
    failure = {
      kind: "access",
      message: copy.access,
      outcome: "rejected",
      recovery: "none",
    };
  else if (status === 404 || status === 405 || status === 410)
    failure = {
      kind: "missing",
      message: intent === "write" ? errorCopy.actionUnavailable : copy.missing,
      outcome: "rejected",
      recovery: "none",
    };
  else if (status === 409)
    failure = {
      kind: "conflict",
      message: copy.conflict,
      outcome: "unknown",
      recovery: "review",
    };
  else if (status === 413 || status === 415)
    failure = {
      kind: "validation",
      message: status === 413 ? errorCopy.fileTooLarge : errorCopy.fileType,
      outcome: "rejected",
      recovery: "review",
    };
  else if (status === 400 || status === 422)
    failure = {
      kind: "validation",
      message:
        reviewedValidation.find(([pattern]) => pattern.test(raw))?.[1] ??
        copy.validation,
      outcome: "rejected",
      recovery: "review",
    };
  else if (status === 429)
    failure = {
      kind: "unavailable",
      message: intent === "write" ? copy.uncertain : errorCopy.busy,
      outcome: intent === "write" ? "unknown" : "read",
      recovery: intent === "write" ? "review" : "retry",
    };
  else if (status === 0)
    failure = {
      kind: "connection",
      message: copy.connection,
      outcome: "read",
      recovery: "retry",
    };
  else
    failure = {
      kind: "unavailable",
      message: copy.unavailable,
      outcome: "read",
      recovery: "retry",
    };
  failure = { ...failure, status, code };
  return new APIError(failure.message, status, failure);
}

/** No retry is performed here. Aborted reads remain cancellation, not failures. */
export async function fetchResponse(
  url: string,
  init?: RequestInit,
): Promise<Response> {
  const method = (init?.method ?? "GET").toUpperCase();
  try {
    return await fetch(url, init);
  } catch (error) {
    if (
      error instanceof Error &&
      error.name === "AbortError" &&
      ["GET", "HEAD"].includes(method)
    )
      throw error;
    throw responseFailure(
      0,
      null,
      ["GET", "HEAD"].includes(method) ? "read" : "write",
    );
  }
}
export async function readResponse<T>(
  response: Response,
  intent: FailureIntent = "read",
): Promise<T> {
  let body: unknown;
  try {
    body = await response.json();
  } catch (error) {
    if (
      intent === "read" &&
      error instanceof Error &&
      error.name === "AbortError"
    )
      throw error;
    throw responseFailure(response.status, null, intent, true);
  }
  if (!response.ok) throw responseFailure(response.status, body, intent);
  return body as T;
}
export async function fetchJSON<T>(
  url: string,
  init?: RequestInit,
): Promise<T> {
  const method = (init?.method ?? "GET").toUpperCase();
  return readResponse<T>(
    await fetchResponse(url, init),
    ["GET", "HEAD"].includes(method) ? "read" : "write",
  );
}
/** Only a confirmed rejection may unfreeze an existing financial request. */
export function isConfirmedRejection(error: unknown): boolean {
  return (
    error instanceof APIError &&
    error.failure.outcome === "rejected" &&
    error.status >= 400 &&
    error.status < 500
  );
}

function safeLocalMessage(value: string): string {
  if (
    !value ||
    value.length > 500 ||
    /endpoint|SQLSTATE|postgres|pgx|syntaxerror|typeerror|unexpected token|(?:unexpected|invalid|malformed) (?:response|payload)|ECONN|stack trace|fetch failed|failed to fetch|networkerror|JSON|idempotenc|csrf|stack trace|\/Users\/|https?:\/\//i.test(
      value,
    )
  )
    return copy.unexpected;
  if (/another Book|different Book/i.test(value)) return errorCopy.bookMismatch;
  if (
    /carrying|journal.*(missing|duplicate)|cannot.*reconcil|source bytes|evidence belongs/i.test(
      value,
    )
  )
    return errorCopy.figuresUnverified;
  return (
    reviewedValidation.find(([pattern]) => pattern.test(value))?.[1] ?? value
  );
}
export function errorMessage(error: unknown): string {
  if (error instanceof Error && verifiedReads.has(error))
    return errorCopy.recordsUnverified;
  if (error instanceof APIError) return error.message;
  if (error instanceof Error) {
    if (/network|failed to fetch|load failed/i.test(error.message))
      return copy.connection;
    if (error.name === "SyntaxError" || error.name === "TypeError")
      return copy.unexpected;
    return safeLocalMessage(error.message);
  }
  return typeof error === "string" ? safeLocalMessage(error) : copy.unexpected;
}
export function presentFailure(error: unknown, context: FailureContext = {}) {
  if (error instanceof Error && verifiedReads.has(error)) {
    return {
      kind: "unexpected" as const,
      message: errorCopy.recordsUnverified,
      outcome: "read" as const,
      recovery: "retry" as const,
      severity: "critical" as const,
      icon: "critical" as const,
    };
  }
  const message = errorMessage(error);
  const known = Object.entries(copy).find(([, text]) => text === message)?.[0];
  const failure: Failure =
    error instanceof APIError
      ? error.failure
      : {
          kind:
            known === "signIn" || known === "sessionEnded"
              ? "access"
              : known
                ? (known as FailureKind)
                : reviewedValidation.some(([, text]) => text === message)
                  ? "validation"
                  : "unexpected",
          message,
          outcome:
            known === "uncertain" || known === "conflict" ? "unknown" : "read",
          recovery:
            known === "signIn"
              ? "sign-in"
              : known === "sessionEnded"
                ? "confirm"
                : known === "missing" || known === "access" || known === "unsupported"
                ? "none"
                : known === "uncertain" || known === "conflict"
                  ? "review"
                  : "retry",
        };
  const nouns = {
    dashboard: "the dashboard",
    accounts: "the accounts",
    balances: "the balances",
    openings: "the opening balances",
    activity: "the activity",
    document: "this document",
    sources: "the sources",
    settings: "the settings",
    statement: "this statement",
    search: "search results",
    approvals: "the approvals",
    app: "Vult",
  };
  const genericRead =
    context.intent === "read" &&
    ["unavailable", "unexpected"].includes(failure.kind) &&
    Object.values(copy).some((text) => text === message);
  const attention =
    ["access", "validation", "conflict", "uncertain"].includes(failure.kind) ||
    failure.outcome === "unknown";
  const file =
    context.resource === "document" ||
    [
      errorCopy.fileTooLarge,
      errorCopy.fileType,
      errorCopy.fileEmpty,
      errorCopy.fileUnreadable,
      errorCopy.receiptSize,
      errorCopy.receiptType,
      errorCopy.fileName,
    ].some((copy) => copy === message);
  const severity: FailureSeverity = attention ? "attention" : "routine";
  const icon: FailureIcon =
    failure.kind === "uncertain" ||
    failure.kind === "conflict" ||
    failure.outcome === "unknown"
      ? "attention"
      : failure.kind === "access"
        ? "access"
        : failure.kind === "connection"
          ? "connection"
          : file
            ? "file"
            : attention
              ? "attention"
              : "generic";

  return {
    ...failure,
    severity,
    icon,
    message: failure.kind === "unsupported" && context.resource
      ? `${notYetAvailable(context.resource)}.`
      : genericRead
      ? `We couldn’t ${context.retained === "view" ? "refresh" : "load"} ${context.resource ? nouns[context.resource] : "this"}.`
      : message,
  };
}

const missingNames = {
  dashboard: ["Dashboard", false],
  accounts: ["Accounts", true],
  balances: ["Balances", true],
  openings: ["Opening balances", true],
  activity: ["Activity", false],
  document: ["This document", false],
  sources: ["Sources", true],
  settings: ["Settings", true],
  statement: ["This statement", false],
  search: ["Search", false],
  approvals: ["Approvals", true],
  app: ["Vult", false],
} as const;

/** Names what isn’t there yet; nothing failed, so nothing is offered to retry. */
function notYetAvailable(resource: keyof typeof missingNames) {
  const [name, plural] = missingNames[resource];
  return `${name} ${plural ? "aren’t" : "isn’t"} available yet`;
}

const stateNouns = {
  dashboard: "the dashboard",
  accounts: "the accounts",
  balances: "the balances",
  openings: "the opening balances",
  activity: "the activity",
  document: "this document",
  sources: "the sources",
  settings: "the settings",
  statement: "this statement",
  search: "search results",
  approvals: "the approvals",
  app: "Vult",
} as const;

/**
 * Page and region failure states: the same policy as presentFailure, with a
 * title that names what failed and a supporting line that never repeats it.
 */
export function presentFailureState(
  error: unknown,
  context: FailureContext = {},
) {
  const failure = presentFailure(error, context);
  const verb =
    context.resource === "app"
      ? "open"
      : context.retained === "view"
        ? "refresh"
        : "load";
  // Being signed out, or a session that ended, is a state, not a failure:
  // say so and offer the way back in.
  const title =
    failure.recovery === "sign-in"
      ? "You’re signed out"
      : failure.recovery === "confirm"
        ? "Session ended"
        : `Couldn’t ${verb} ${context.resource ? stateNouns[context.resource] : "this"}`;
  const detail =
    failure.severity === "critical"
      ? failureStateCopy.recordsUnverified
      : failure.kind === "connection"
        ? failureStateCopy.connection
        : failure.kind === "unsupported"
          ? failureStateCopy.unsupported
          : failure.kind === "missing"
            ? failureStateCopy.missing
            : failure.recovery === "sign-in"
              ? failureStateCopy.signedOut
              : failure.recovery === "confirm"
                ? failureStateCopy.sessionEnded
                : failure.kind === "access"
                ? failureStateCopy.access
                : failure.message === errorCopy.busy
                  ? failureStateCopy.busy
                  : failure.kind === "unavailable" ||
                      failure.kind === "unexpected"
                    ? failureStateCopy.unavailable
                    : failure.message;
  if (failure.kind === "unsupported")
    return {
      ...failure,
      title: context.resource
        ? notYetAvailable(context.resource)
        : "This view isn’t available yet",
      detail: undefined,
    };
  return { ...failure, title, detail };
}

/** A routine refresh must not hide an independently verified integrity failure. */
export function mostUrgentFailure(...errors: unknown[]): unknown {
  const rank = { routine: 0, attention: 1, critical: 2 };
  return errors
    .filter(Boolean)
    .reduce<unknown>(
      (current, next) =>
        !current ||
        rank[presentFailure(next).severity] >
          rank[presentFailure(current).severity]
          ? next
          : current,
      undefined,
    );
}

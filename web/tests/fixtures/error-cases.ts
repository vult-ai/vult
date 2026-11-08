import {
  APIError,
  responseFailure,
  type FailureContext,
} from "../../src/lib/errors";
import { errorCopy } from "../../src/lib/error-copy";
export type CopyExample = {
  id: string;
  label: string;
  group: "Loading and access" | "Saving" | "Files" | "Field validation";
  error: unknown;
  context: FailureContext;
  retry?: boolean;
  field?: { label: string; value: string };
};
const validation = (text: string) => new APIError(text, 400);
export const copyExamples: CopyExample[] = [
  {
    id: "first-load",
    label: "First load fails",
    group: "Loading and access",
    error: responseFailure(503, {}),
    context: { intent: "read", resource: "dashboard" },
    retry: true,
  },
  {
    id: "connection",
    label: "Connection unavailable",
    group: "Loading and access",
    error: responseFailure(0, {}),
    context: { intent: "read", resource: "dashboard" },
    retry: true,
  },
  {
    id: "busy",
    label: "Too many requests",
    group: "Loading and access",
    error: responseFailure(429, {}),
    context: { intent: "read" },
    retry: true,
  },
  {
    id: "signed-out",
    label: "Sign-in needed",
    group: "Loading and access",
    error: responseFailure(401, {}),
    context: { intent: "read" },
  },
  {
    id: "permission",
    label: "Permission missing",
    group: "Loading and access",
    error: responseFailure(403, {}),
    context: { intent: "read" },
  },
  {
    id: "missing",
    label: "Item unavailable",
    group: "Loading and access",
    error: responseFailure(404, {}),
    context: { intent: "read", resource: "document" },
  },
  {
    id: "validation",
    label: "Save needs a correction",
    group: "Saving",
    error: responseFailure(400, {}, "write"),
    context: { intent: "write" },
  },
  {
    id: "conflict",
    label: "Change needs review",
    group: "Saving",
    error: responseFailure(409, {}, "write"),
    context: { intent: "write" },
  },
  {
    id: "large-file",
    label: "File too large; limit not supplied",
    group: "Files",
    error: responseFailure(413, {}, "write"),
    context: { intent: "write" },
  },
  {
    id: "file-type",
    label: "Unsupported upload type",
    group: "Files",
    error: responseFailure(415, {}, "write"),
    context: { intent: "write" },
  },
  {
    id: "empty-file",
    label: "Empty file",
    group: "Files",
    error: validation(errorCopy.fileEmpty),
    context: { intent: "action" },
  },
  {
    id: "unreadable-file",
    label: "Unreadable file",
    group: "Files",
    error: validation(errorCopy.fileUnreadable),
    context: { intent: "action" },
  },
  {
    id: "receipt-size",
    label: "Receipt outside its known size limit",
    group: "Files",
    error: validation(errorCopy.receiptSize),
    context: { intent: "write" },
  },
  {
    id: "receipt-type",
    label: "Receipt needs a different file type",
    group: "Files",
    error: validation(errorCopy.receiptType),
    context: { intent: "write" },
  },
  {
    id: "file-name",
    label: "Receipt filename needs changing",
    group: "Files",
    error: validation(errorCopy.fileName),
    context: { intent: "write" },
  },
  {
    id: "amount",
    label: "Amount must be positive",
    group: "Field validation",
    error: validation(errorCopy.amountPositive),
    context: {},
    field: { label: "Amount", value: "0" },
  },
  {
    id: "amount-format",
    label: "Amount contains symbols or separators",
    group: "Field validation",
    error: validation(errorCopy.amountFormat),
    context: {},
    field: { label: "Amount", value: "$1,000" },
  },
  {
    id: "date",
    label: "Invalid date",
    group: "Field validation",
    error: validation(errorCopy.dateInvalid),
    context: {},
    field: { label: "Date", value: "October 40" },
  },
  {
    id: "rate-review",
    label: "Rate does not match the selected entry",
    group: "Field validation",
    error: validation(errorCopy.rateReview),
    context: { intent: "write" },
  },
  {
    id: "entry-balance",
    label: "Journal entry does not balance",
    group: "Field validation",
    error: validation(errorCopy.entryUnbalanced),
    context: { intent: "write" },
  },
  {
    id: "amounts-match",
    label: "Same-currency payment amounts differ",
    group: "Field validation",
    error: validation(errorCopy.sameCurrencyAmounts),
    context: { intent: "write" },
  },
  {
    id: "opening-currency",
    label: "Opening balance uses the wrong account currency",
    group: "Field validation",
    error: validation(errorCopy.openingCurrency),
    context: { intent: "write" },
  },
  {
    id: "recorded-balance",
    label: "Payment exceeds the recorded account balance",
    group: "Field validation",
    error: validation(errorCopy.cashInsufficient),
    context: { intent: "write" },
  },
  {
    id: "wrong-book",
    label: "Records belong to another Book",
    group: "Saving",
    error: validation(errorCopy.bookMismatch),
    context: { intent: "write" },
  },
  {
    id: "unverified-records",
    label: "Source verification fails",
    group: "Saving",
    error: validation(errorCopy.figuresUnverified),
    context: { intent: "read" },
  },
];

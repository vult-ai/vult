/** Approved user-facing copy. Conditions and recovery policy stay in errors.ts. */
export const errorCopy = {
  recordsUnverified: "We couldn’t verify these records.",
  reviewUnavailable: "We couldn’t check this. Review before trying again.",
  connection: "We couldn’t connect. Check the internet connection.",
  unavailable: "We couldn’t finish that. Try again in a moment.",
  featureUnavailable: "This view isn’t available yet.",
  access: "You don’t have permission to do that.",
  signIn: "Sign in to continue.",
  sessionEnded: "Session ended. Confirm it’s you to continue.",
  missing: "We can’t open this right now.",
  actionUnavailable: "We can’t make that change right now.",
  validation: "Check the details and try again.",
  conflict: "Check the saved version before trying again.",
  uncertain: "The change may already be saved. Check before trying again.",
  busy: "Give us a moment, then try again.",
  fileTooLarge: "This file is too large. Choose a smaller one.",
  fileType: "This file type isn’t supported.",
  fileEmpty: "This file is empty. Choose another copy.",
  fileUnreadable: "We couldn’t read this file. Try another copy.",
  receiptSize: "Choose a receipt with content, up to 10 MB.",
  receiptType: "Use a PDF, PNG or JPG for the receipt.",
  fileName: "Rename the file without slashes, using up to 180 characters.",
  rateExpired: "That exchange rate expired. Refresh it and try again.",
  rateReview: "Refresh the exchange rate for this entry.",
  entryUnbalanced: "The amounts on both sides need to match.",
  amountPositive: "Enter an amount greater than zero.",
  amountFormat: "Use numbers without commas or currency symbols.",
  dateInvalid: "Enter a valid date.",
  currencies: "Check the currencies selected for this payment.",
  openingCurrency:
    "Use the selected account’s currency for its opening balance.",
  sameCurrencyAmounts:
    "These amounts should match. Record any fees separately.",
  cashInsufficient: "This amount is more than the account’s recorded balance.",
  figuresUnverified:
    "We couldn’t verify these records. Review the source files.",
  bookMismatch: "These records belong to a different Book.",
} as const;

/**
 * Supporting lines for page and region failure states. The state's title names
 * what failed, so these give the reason or next step and never repeat the
 * action button. They state only what the failure proves; they never guess.
 */
export const failureStateCopy = {
  connection: "Check the internet connection.",
  unavailable: "This is usually temporary.",
  busy: "Vult is busy right now. This is usually temporary.",
  unsupported: "This view isn’t available yet.",
  missing: "Check the address, or open it again from Vult.",
  access: "This account doesn’t have access here.",
  signIn: "You’re signed out.",
  signedOut: "Sign in to open Books.",
  sessionEnded: "For security, Vult asks you to confirm it’s you.",
  recordsUnverified:
    "We couldn’t verify these records. Review them before relying on these figures.",
} as const;

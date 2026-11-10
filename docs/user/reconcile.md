---
{"id":"reconcile","title":"Reconcile statements","description":"Compare each bank and card statement with your Book, accept matches, and sign the statement off.","group":"Use your Book","order":7,"keywords":["reconcile","reconciliation","statement","bank","card","match","outstanding","difference","duplicate"]}
---

## How it works {#how-it-works}

When you add a bank or card statement in Sources, your agent imports it into **Reconcile**: the statement's period, its opening and closing balances, and every row exactly as printed. A statement never posts anything by itself. Vult compares it with the entries in your Book, row by row.

## Matches {#matches}

A match says a statement row and an entry in the Book are the same money. Vult suggests matches when an entry has the same amount within a week of the row, but nothing is recorded until you or your agent accepts one. One row can match several entries, and several rows one entry. **Undo match** takes a match back; its history stays.

An entry that's matched can't be reversed until the match is undone.

## What the comparison shows {#comparison}

- **Matched:** rows that are in the Book.
- **On the statement, not in the Book:** rows with no entry yet, such as a bank fee. Your agent posts the missing entry, then links the row to it as an adjustment.
- **In the Book, not on a statement yet:** entries posted before the statement's cutoff that the bank hasn't shown yet, such as a payment that clears next month. These are normal.
- **Remaining difference:** the closing balance minus the opening balance and the explained statement movements. When it's zero and every settled row is explained, the statement can be marked reconciled.
- **Repeated from another statement:** the row still belongs in this statement's opening-to-closing balance. Its explanation comes from the original row, so it never allocates the same Book movement twice.
- Pending and cancelled rows do not affect the statement's settled movement.

A row your agent has only proposed to match or adjust still counts as not in the Book until you approve it.

## Sign it off {#review}

**Mark reconciled** records the exact comparison you signed off. Vult refuses it while anything is unexplained. **Needs work** records what's left to do, so your agent can pick it up. You can review a statement again later; each review is kept. If a later change removes an explanation, the statement needs work again; the earlier signed comparison stays in its history.

Foreign-currency accounts reconcile in their own currency, including currencies with no decimal places.

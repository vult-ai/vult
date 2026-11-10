---
{"id":"record-transactions","title":"Record and correct entries","description":"How your agent records entries, when it asks you first, and how mistakes are corrected without rewriting history.","group":"Use your Book","order":4,"keywords":["record","journal","entry","approve","reject","approval","proposal","reverse","reversal","correct","receipt","source","evidence","dates"]}
---

## How entries get recorded {#how-entries-are-recorded}

Your agent records every entry as balanced debits and credits. It chooses the accounts, the amounts and the dates, and can attach the original documents. Vult refuses an entry that doesn't balance or that uses an account or date it can't accept, so a mistake is caught before it reaches your Book.

Ask your agent in plain words, for example "Record this Stripe payout" or "Add these receipts from October." With write access, it can post straightforward entries directly.

## Approve what your agent asks about {#approvals}

When your agent is unsure, or you ask it to check with you first, it prepares the entry instead of posting it. The entry waits in **Approvals**:

1. Open the entry to see each line, its debit or credit, and each account's balance before and after.
2. Read the agent's question and the assumptions it made, and check any attached sources.
3. Choose **Approve** to post exactly that entry, or **Reject** with a reason.

Vult asks you to have signed in recently before a decision. Approval posts the entry and records who approved it and why. If the Book changed in a way that affects the entry, such as a closed period, Vult refuses the approval and the agent prepares a fresh one. Prepared entries don't expire.

## Use the right dates {#dates}

Each entry has an **economic date** (when the event happened), an **accounting date** (the period it belongs to) and the time Vult saved it. Reports and filters use the accounting date. Recording an older event today can change an older report, which is expected.

## Keep the original documents {#sources}

Your agent can add a receipt, invoice or statement and cite where in it the figures came from. Adding a source to an entry, or correcting a citation, appends history; it never changes the posted amounts. Ask your agent to list entries that are missing a source when you want to tidy up.

## Correct a mistake {#correct-a-mistake}

Posted entries are never edited or deleted. To correct one, ask your agent to reverse it. A reversal mirrors every line of the original and links back to it. The agent can then record the corrected entry. Both the mistake and its correction stay visible in Activity.

Renaming an account or changing its code never changes the entries that use it.

## Close a period {#close-a-period}

When a period's entries are complete and checked against your statements, close it so nothing more is posted into it. In Vult, open the Book's **Settings**, then **Accounting**, choose the first and last day under **Closing periods**, and select **Review and close**. Vult first shows what's still open in those dates: statements that aren't reconciled, and income or expense entries with no evidence or no payee. Transfers between accounts, opening balances and reversals never count.

- If nothing is open, close it. An agent with write access can also close a period with nothing open.
- If something is open, you can still close it, but only you, and with a reason. The reason shows on that period's reports.

Closing isn't permanent. Select **Reopen** with a reason to let a period accept entries again; the reason shows on that period's reports, and the history keeps every close and reopening. Agents can't reopen a period; they can ask you to, through Approvals. Closing a year works the same way and posts no closing entries: reports work out retained earnings.

On your own computer you can also close a period from the terminal:

```sh
vult period close --book BOOK_ID --from 2026-01-01 --through 2026-01-31
```

A period closed from the terminal records no reason.

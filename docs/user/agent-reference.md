---
{"id":"agent-reference","title":"Agent and CLI reference","description":"The tools agents can use, how amounts and retries work, and the owner commands.","group":"Reference","order":11,"keywords":["mcp","tools","cli","commands","request key","retry","amount","minor unit","scale","post_journal","prepare_operation","vult"]}
---

## Agent tools {#tools}

Read connections can use:

- `get_book`, `list_accounts`, `get_journal`, `list_journals`
- `get_balance`, `get_trial_balance`, `get_request_outcome`
- `list_closed_periods`, `get_exchange_rate`, `list_starter_charts`
- `list_payees`, `get_entry_payee`, `list_recurring_charges`, `get_cash_flow`
- `list_statements`, `get_statement_reconciliation`, `suggest_statement_matches`, `get_period_review`
- `get_operation_proposal`, `get_document`, `list_documents`
- `get_entry_documents`, `get_source_usage`, `list_entries_missing_evidence`

Write connections can also use:

- `create_account`, `revise_account`, `retire_account`, `reactivate_account`, `apply_starter_chart`, `set_book_logo`
- `create_payee`, `revise_payee`, `set_entry_payee`
- `import_statement`, `match_statement`, `revoke_statement_match`, `record_statement_adjustment`, `mark_statement_duplicate`, `review_statement`
- `close_period` for a period with nothing open; reopening, and closing with open items, are prepared for the owner
- `post_journal`, `reverse_journal`, `prepare_operation`
- `add_document`, `correct_source_citation`, `attach_evidence`, `detach_evidence`

Tools never take a Book ID. The Book comes from the connection.

## Amounts {#amounts}

Amounts are whole numbers in the Book's smallest unit, written as strings. In a USD Book with scale 2, `"1250"` means $12.50. Each line is either a debit or a credit, and every entry must balance. Decimals and exponents are rejected, so no amount is ever rounded silently.

## Retries {#retry-identity}

Every write carries a `request_key`. Retrying the same request with the same key returns the saved result instead of recording twice. Reusing a key with different input is refused.

If a reply is lost, call `get_request_outcome` with the original key. An unresolved outcome doesn't mean the write failed. Retry only the original request with the original key; never make up a new key for an uncertain write.

## Prepared entries {#prepared-entries}

`prepare_operation` checks a journal entry or reversal, or an account change (create, revise, retire with its transfer, reactivate, or a starter chart), and holds it for the owner without recording it. Its reply includes a link to the entry in **Approvals** and a short message the agent can show you. After you decide, `get_operation_proposal` returns the posted entry, your reason for rejecting it, or `stale` when a fresh proposal is needed. An agent can have up to 128 entries waiting in one Book.

## Owner commands {#operator-commands}

On your own computer, the owner manages Vult from the terminal:

```sh
vult status
vult book create --name "Personal" --currency USD --scale 2
vult book list
vult connection create --name "Example agent" --book BOOK_ID --access write
vult connection list
vult connection revoke CONNECTION_ID
vult open
vult period close --book BOOK_ID --from 2026-01-01 --through 2026-01-31
vult verify --book BOOK_ID
vult export --book BOOK_ID --output journal.json
vult backup --output books.age
vult restore --input books.age
vult uninstall
vult help
```

Owner commands ask for administrator approval. Agents can't run them.

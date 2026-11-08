# Selected journal postings

The bundled interface can request the Book-currency posting effects of up to
100 distinct entries. The read returns every line for that selection, or rejects
the whole request. Missing entries, entries in another Book and entries outside
the owner's scope all return the same not-found response.

This is a minimal contribution projection. Complete entries retain their dates,
native movements, contexts, valuation inputs and document relationships through
the journal and archive contracts.

Amounts remain integer strings, including known zero Book amounts. The wire
field `account_code` contains the stable account identifier; assigned GL codes
are separate account attributes. A rename or recode does not change the
projection's references or recorded effects.

Entries are sorted by immutable identifier and lines by posting position. The
`content_sha256` identifies the canonical JSON projection excluding that digest
field. It is an output fingerprint, not an attestation by an external source.

The read has a 10,000-line bound and never truncates an entry. Missing selections
are rejected before applying that bound. Browser access requires an owner session
that remains live through the read, rejects bearer credentials and uses `no-store`.
The data is loaded in one bounded line query within the selected Book; it does
not export or scan the Book's full journal for each visible page.

## Complete entries and document relationships

A complete entry read retains its economic/accounting dates, recorded timestamp,
line IDs, exact reversal references, native movements, contexts and valuation
inputs. These facts are not flattened into one foreign-currency pair. Its
versioned response identifies the Book and Book unit and contains the full entry.

The browser journal envelope can include `recorded_by` for a resolved actor.
An agent attribution contains `kind: "agent"`, the Book connection's saved
name, and `connection_id`. Revocation keeps that attribution readable.
Owner approval keeps the preparing agent as the journal actor.
When the recorded actor is the signed-in owner, the attribution has
`kind: "owner"` and the owner's display name, or “You” if none is set.
The entry panel uses the signed-in profile for the owner's avatar and the
connection name for an agent's initials.

Restored journal entries retain the immutable actor ID. A data-only restore
can leave that ID without a connection or current owner identity. In that
case `recorded_by` is absent. An import provider requires recorded import
provenance on the entry.

Document reads keep source citations and role-bearing evidence in separate
lists, each with its own cursor and completion flag. Current relationships and
append-only history are distinct queries. These reads share the agent contract;
a statement citation does not become receipt evidence or a stored file download.

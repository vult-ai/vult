# Feedback

Version 1. This standard covers how the interface tells you what happened:
loading, empty and failure states, notices after a change, and confirmations
before a destructive one. You can see every case, in light and dark and at
phone width, in the states gallery at `web/tests/fixtures/states.html`.

## Principle

**One anatomy for every state, and emphasis only where it is earned.** A
state is an icon, a title, one line and its actions. Colour is kept for the
one case that needs it, a failure that could make your numbers wrong, so that
when something is red it matters.

## States

Loading, empty and failure states share one layout: a quiet icon tile, a
title, one supporting line, then at most two actions.

| Scope | Where | Size |
| -- | -- | -- |
| Page | Replaces a whole page | 40px icon tile, centred in the canvas |
| Region | Fills a table, list, panel or card | 36px icon tile |
| Inline | One line inside a panel | Text only, no icon |

### Loading

`LoadingState`. The label names what is loading ("Loading transactions…").
It never replaces content that is already on screen; a refresh keeps the old
content until the new arrives.

### Empty

`EmptyState`. The title names what is missing, with no full stop:

* **Nothing yet**: "No transactions yet". It may have an icon and an action
  that creates the first one.
* **Nothing matches**: "No matching transactions". No icon, because it is a
  result rather than a place; the action, if any, clears the filters.

The detail line, when there is one, says what to do next as a sentence.

### Failure

`FailureState` replaces content that could not be read. `ErrorMessage` sits
beside content that still works, such as a form whose save failed.

Every failure is translated by the shared failure policy (`lib/errors.ts`)
into a severity and a recovery. Pages never choose either, and server error
text is never shown as it is.

| Severity | When | Emphasis |
| -- | -- | -- |
| Routine | A read or connection failed | Quiet |
| Attention | Something needs a decision before continuing | Stronger text |
| Critical | A verified integrity check failed: the numbers may be wrong | Error colour, announced at once |

| Recovery | Action |
| -- | -- |
| Retry | "Try again" |
| Sign in | "Sign in" |
| Review | "Review", to check whether a change was already saved |
| None | No action |

A change whose answer was lost is never resent silently: it is frozen and
offered as "Send again" with the same request, so it cannot be recorded twice.

## Notices

`Notice` reports something that is not a failure.

| Tone | Use it for |
| -- | -- |
| Success | A change that completed: "Book name saved." |
| Info | Something worth knowing |
| Caution | A condition to respect before acting |

* Copy is one or two whole sentences, each ending with a full stop, and no
  exclamation marks. Say what changed in the past tense, then what stays the
  same if that matters: "Address saved. Earlier links still work."
* Notices sit inline, next to what they report, or float when the change has
  no place on screen. A notice can carry one action or be dismissible.
* Success and info notices are announced through one shared live region, so
  screen readers read them once. Cautions are read with the page instead.

## Buttons while working

A button keeps its label while its action runs and shows progress with
`busy`. It never changes to "Saving…" or "Loading…".

## Confirmations

`ConfirmAction` (or `ConfirmFooter` inside a larger dialog) guards every
destructive or irreversible step.

* The title is the question: "Delete this file?"
* One line says what happens, adding "You can't undo this." when it is
  permanent. Specific evidence, such as the Books that lose access, follows.
* Errors appear above the actions. The actions are Cancel, then the confirm
  verb in the error colour.
* While the step runs, Escape, the close button and Cancel are unavailable.

The dialog itself follows the [dialog standard](dialogs.md).

## Enforcement

Two scripts run with the design checks.

* `scripts/check-feedback.mjs` rejects hand-built notices and live regions,
  destructive buttons outside the shared confirmation, button labels that
  change while pending, and notice copy without a closing full stop.
* `scripts/check-error-handling.mjs` rejects hand-built loading lines,
  error surfaces and empty-table text, pages that set a failure's severity or
  icon, and server error text shown as it is.

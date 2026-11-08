import { React } from "./dom.mjs";

export async function openFilters() {
  if (!document.querySelector('select[aria-label="Filter transaction type"]'))
    await React.act(async () =>
      document
        .querySelector('button[aria-label^="Filter transactions"]')
        .click(),
    );
}

export async function startTransaction(window, label) {
  await React.act(async () =>
    document.querySelector('button[aria-label="Manage transactions"]').click(),
  );
  await React.act(async () => {
    const item = [...document.querySelectorAll('[role="menuitem"]')].find(
      (el) => el.textContent === "New transaction",
    );
    item.focus();
    item.dispatchEvent(
      new window.KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }),
    );
  });
  await React.act(async () =>
    [...document.querySelectorAll('[role="menuitem"]')]
      .find((el) => el.textContent === label)
      .click(),
  );
}

// Observe the real workspace's Book-scoped snapshot through its existing Table
// seam. Financial freshness checks must not require balance reports in Settings.
export function BookSnapshotProbe({ state, book, toolbar, notice }) {
  return React.createElement(
    "div",
    null,
    toolbar,
    notice,
    React.createElement(
      "output",
      { "data-testid": "book-snapshot-cash", "data-book-id": book.id },
      state.summary.cash_minor,
    ),
  );
}
export const snapshotCash = () =>
  document.querySelector('[data-testid="book-snapshot-cash"]')?.textContent;

export const ledgerTrigger = () =>
  document.querySelector('button[aria-label^="Switch Book, current Book"]');
export async function openLedgers() {
  if (
    document.querySelector('[role="dialog"][aria-label="Transaction filters"]')
  )
    await React.act(async () =>
      document
        .querySelector('button[aria-label^="Filter transactions"]')
        .click(),
    );
  await React.act(async () => ledgerTrigger().click());
}
export async function chooseLedger(name) {
  await openLedgers();
  await React.act(async () =>
    [...document.querySelectorAll('[role^="menuitem"]')]
      .find((item) => {
        const label = item.cloneNode(true);
        label.querySelectorAll('[aria-hidden="true"]').forEach((decoration) => decoration.remove());
        return label.textContent.trim() === name;
      })
      .click(),
  );
}
export async function startNewLedger() {
  await openLedgers();
  await React.act(async () =>
    [...document.querySelectorAll('[role="menuitem"]')]
      .find((item) => item.textContent.trim() === "Create new Book")
      .click(),
  );
  await React.act(
    async () => new Promise((resolve) => requestAnimationFrame(resolve)),
  );
}
export async function ledgerCount() {
  const current = ledgerTrigger().textContent.trim();
  await openLedgers();
  const items = [...document.querySelectorAll('[role^="menuitem"]')];
  const count = items.filter(
    (item) =>
      !["Create new Book", "Manage Books"].includes(item.textContent.trim()),
  ).length;
  await React.act(async () =>
    items.find((item) => item.textContent.trim() === current).click(),
  );
  return count;
}

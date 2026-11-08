import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Synthetic",
  currency: "USD",
  scale: 2,
  accounting_timezone: "UTC",
};
const ids = [
  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
];
const settle = () =>
  React.act(async () => new Promise((resolve) => setTimeout(resolve, 80)));

for (const mobile of [false, true])
  test(
    `${mobile ? "modal above the account" : "docked"} entry inspection keeps Accounts, dates, ledger page, scroll and exact row focus through close and Back`,
    { timeout: 15000 },
    () =>
      withDOM(async ({ window, render }) => {
        const m = await loadComponent("unit/helpers/accounts-entry.tsx");
        window.history.replaceState(
          null,
          "",
          `/accounts?from=2026-01-01&to=2026-10-27`,
        );
        const href = window.location.href;
        const ledger = {
          accountCode: "bank",
          range: { from: "2026-01-01", to: "2026-10-27" },
          positiveSide: "debit",
          opening: 0n,
          debits: 60n,
          credits: 0n,
          closing: 60n,
          lines: Array.from({ length: 60 }, (_, i) => ({
            id: `posting-${i}`,
            transactionId: ids[i % 2],
            description: `Synthetic posting ${i + 1}`,
            date: "2026-03-01",
            postingDate: "2026-03-01",
            debit: 1n,
            credit: 0n,
            balance: BigInt(i + 1),
          })),
        };
        let control,
          readerMounts = 0;
        function Reader({ id }) {
          React.useEffect(() => {
            readerMounts++;
          }, []);
          React.useLayoutEffect(() => {
            // Model queued grid scroll after adding the desktop inspector pane.
            if (!mobile)
              document.querySelector(
                '[data-vult-view-scroll="account-ledger:body"]',
              ).scrollTop = 0;
          }, []);
          return React.createElement(
            "div",
            null,
            React.createElement("p", null, id),
            React.createElement("input", {
              "aria-label": "Reader draft",
              defaultValue: "unchanged",
            }),
          );
        }
        function Harness() {
          const { detail, show } = m.useEntryPreview(
            book,
            false,
            ids.map((id) => ({ id })),
          );
          const trigger = React.useRef(null);
          const [docked, setDocked] = React.useState(!mobile);
          control = { show, setDocked };
          const content = React.createElement(
            "div",
            null,
            React.createElement(m.AccountLedgerView, {
              ledger,
              book,
              name: "Operating",
              onInspect: (id, target) => {
                trigger.current = target;
                show(id, detail ? "replace" : "push");
              },
            }),
          );
          return React.createElement(
            m.AccountsEntryProvider,
            {
              value: {
                id: detail,
                pending: false,
                returnTo: trigger.current,
                onClose: () => window.history.back(),
                render: (id) => React.createElement(Reader, { id, key: id }),
              },
            },
            React.createElement(
              "div",
              { className: "collection-inspector" },
              React.createElement("div", null, "Account tree"),
              mobile
                ? React.createElement(
                    m.Dialog,
                    {
                      open: true,
                      title: "Operating",
                      description: null,
                      onOpenChange() {},
                      className: "underlying-account",
                    },
                    content,
                  )
                : content,
              React.createElement(m.AccountsEntryInspector, { docked }),
            ),
          );
        }
        await render(React.createElement(Harness));
        await settle();
        const dock = document.querySelector(".account-entry-inspector");
        assert.equal(dock.dataset.open, "false");
        assert.match(dock.textContent, /Select a transaction/);
        const emptyPanel = dock.querySelector('[data-testid="entry-preview"]');
        const next = document.querySelector('button[aria-label="Next page"]');
        await React.act(() => next.click());
        await settle();
        const page = document.querySelector('input[aria-label="Go to page"]');
        assert.equal(page.value, "2");
        const table = document.querySelector('[data-testid="account-ledger"]');
        const viewport = table.querySelector("[data-vult-view-scroll]");
        assert.ok(viewport);
        viewport.scrollTop = 137;
        viewport.scrollLeft = 51;
        const trigger = document.querySelector(
          'button[aria-label="View entry and source evidence: Synthetic posting 27"]',
        );
        assert.ok(trigger);
        await React.act(() => {
          trigger.focus();
          trigger.click();
        });
        await settle();
        assert.equal(window.location.pathname, "/accounts");
        assert.equal(
          new URL(window.location.href).searchParams.get("from"),
          "2026-01-01",
        );
        assert.ok(new URL(window.location.href).searchParams.get("entry"));
        assert.ok(
          document.querySelector('[data-testid="account-ledger"]') === table,
          "ledger stays mounted",
        );
        assert.equal(page.value, "2");
        assert.equal(viewport.scrollTop, 137);
        assert.equal(viewport.scrollLeft, 51);
        assert.ok(
          table.querySelector('tr[data-details-open="true"]'),
          "inspected posting is highlighted",
        );
        if (mobile)
          assert.ok(
            document.querySelector('.entry-context-dialog[role="dialog"]'),
          );
        else
          assert.match(
            document.querySelector(".account-entry-inspector").textContent,
            /Details/,
          );
        const close = mobile
          ? document.querySelector(
              '.entry-context-dialog button[aria-label="Close dialog"]',
            )
          : document.querySelector('button[aria-label="Close entry details"]');
        await React.act(() => close.click());
        await React.act(
          async () => new Promise((resolve) => setTimeout(resolve, 260)),
        );
        assert.equal(window.location.href, href);
        assert.equal(
          dock.dataset.open,
          "false",
          "closing an entry gives its space back to the ledger",
        );
        if (!mobile) {
          assert.ok(
            dock.querySelector('[data-testid="entry-preview"]') === emptyPanel,
            "the Details panel stays mounted across entry selection",
          );
          assert.match(dock.textContent, /Select a transaction/);
        }
        assert.ok(
          document.querySelector('[data-testid="account-ledger"]') === table,
          "ledger stays mounted",
        );
        assert.equal(page.value, "2");
        assert.equal(viewport.scrollTop, 137);
        assert.equal(viewport.scrollLeft, 51);
        assert.ok(
          trigger.closest("tr").contains(document.activeElement),
          `focus returns to ledger control: active=${document.activeElement?.tagName}/${document.activeElement?.getAttribute("data-record-id")} expected=${trigger.closest("tr")?.getAttribute("data-record-id")} connected=${trigger.isConnected} sameLabel=${document.activeElement?.getAttribute("aria-label") === trigger.getAttribute("aria-label")}`,
        );
        await React.act(() => trigger.click());
        await settle();
        await React.act(async () => {
          window.history.back();
          await new Promise((resolve) => setTimeout(resolve, 90));
        });
        await settle();
        assert.equal(window.location.href, href);
        assert.equal(page.value, "2");
        assert.equal(viewport.scrollTop, 137);
        assert.ok(
          trigger.closest("tr").contains(document.activeElement),
          `focus returns to ledger control: active=${document.activeElement?.tagName}/${document.activeElement?.getAttribute("data-record-id")} expected=${trigger.closest("tr")?.getAttribute("data-record-id")} connected=${trigger.isConnected} sameLabel=${document.activeElement?.getAttribute("aria-label") === trigger.getAttribute("aria-label")}`,
        );
        if (mobile) return;
        await React.act(() => trigger.click());
        await settle();
        const input = document.querySelector(
          'input[aria-label="Reader draft"]',
        );
        input.value = "retained draft";
        const mounts = readerMounts;
        await React.act(() => control.setDocked(mobile));
        await settle();
        assert.ok(
          document.querySelector('input[aria-label="Reader draft"]') === input,
          "reader stays mounted",
        );
        assert.equal(input.value, "retained draft");
        assert.equal(
          readerMounts,
          mounts,
          "responsive presentation does not remount the reader",
        );
        assert.ok(
          document.querySelector('[data-testid="account-ledger"]') === table,
          "ledger stays mounted",
        );
      }),
  );

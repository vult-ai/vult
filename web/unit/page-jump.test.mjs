import assert from "node:assert/strict";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const field = () => document.querySelector('input[aria-label="Go to page"]');
const button = (label) =>
  document.querySelector(`button[aria-label="${label}"]`);
async function type(window, value) {
  await React.act(async () => {
    Object.getOwnPropertyDescriptor(
      window.HTMLInputElement.prototype,
      "value",
    ).set.call(field(), value);
    field().dispatchEvent(new window.Event("input", { bubbles: true }));
  });
}
async function key(window, value) {
  await React.act(async () =>
    field().dispatchEvent(
      new window.KeyboardEvent("keydown", {
        key: value,
        bubbles: true,
        cancelable: true,
      }),
    ),
  );
}
const records = Object.freeze(
  Array.from({ length: 549 }, (_, index) =>
    Object.freeze({ id: String(index), label: `Record ${index + 1}` }),
  ),
);
const rowIds = () =>
  [...document.querySelectorAll('tr[data-testid^="record-"]')].map(
    (row) => row.dataset.testid.slice("record-".length),
  );

test("shared collections jump directly to page 22 without visiting intermediate pages", async () => {
  await withDOM(async ({ render, window }) => {
    const { CollectionTable } = await loadComponent(
      "src/design-system/collection-table.tsx",
    );
    const view = (rows = records, pageResetKey = "all") =>
      React.createElement(CollectionTable, {
        ariaLabel: "Records",
        testId: "jump-records",
        rows,
        columns: [{ id: "label", label: "Name", width: 240, minWidth: 100, isRowHeader: true }],
        rowId: (row) => row.id,
        rowLabel: (row) => row.label,
        rowTestId: (row) => `record-${row.id}`,
        renderCell: (row) => row.label,
        emptyState: "Empty",
        pageResetKey,
      });
    await render(view());
    assert.equal(field().value, "1");
    await React.act(async () => field().focus());
    assert.equal(field().selectionStart, 0);
    assert.equal(
      field().selectionEnd,
      1,
      "focusing selects the whole page number",
    );
    await type(window, "2");
    assert.deepEqual(
      rowIds(),
      records.slice(0, 25).map((row) => row.id),
    );
    await type(window, "22");
    assert.deepEqual(
      rowIds(),
      records.slice(0, 25).map((row) => row.id),
      "typing never navigates",
    );
    await key(window, "Enter");
    assert.deepEqual(
      rowIds(),
      records.slice(525).map((row) => row.id),
    );
    assert.equal(field().value, "22");
    assert.equal(button("Next page").disabled, true);
    await React.act(async () => button("Previous page").click());
    assert.deepEqual(
      rowIds(),
      records.slice(500, 525).map((row) => row.id),
    );
    assert.equal(
      field().value,
      "21",
      "arrow navigation synchronizes the draft",
    );
    await type(window, "15");
    await render(view(records.slice(0, 10), "filtered"));
    assert.equal(
      field().value,
      "1",
      "filter and page-count changes discard stale drafts",
    );
    assert.deepEqual(
      rowIds(),
      records.slice(0, 10).map((row) => row.id),
    );
    assert.equal(button("Previous page").disabled, true);
    assert.equal(button("Next page").disabled, true);
  });
});

test("page edits commit on blur, cancel with Escape and remain inside the available range", async () => {
  await withDOM(async ({ render, window }) => {
    const { PageJump } = await loadComponent("src/design-system/page-jump.tsx");
    const changes = [];
    const escaped = [];
    function Example({ disabled = false }) {
      const [page, setPage] = React.useState(1);
      return React.createElement(
        "div",
        { onKeyDown: (event) => escaped.push(event.key) },
        React.createElement(PageJump, {
          currentPage: page,
          pageCount: 22,
          isDisabled: disabled,
          onPageChange: (next) => {
            changes.push(next);
            setPage(next);
          },
        }),
        React.createElement(
          "button",
          { "aria-label": "Other control" },
          "Other",
        ),
      );
    }
    await render(React.createElement(Example));
    await React.act(async () => field().focus());
    await type(window, "12");
    await React.act(async () => button("Other control").focus());
    assert.deepEqual(changes, [12]);
    assert.equal(field().value, "12");
    await React.act(async () => field().focus());
    await type(window, "7");
    await key(window, "Escape");
    assert.equal(field().value, "12");
    assert.deepEqual(changes, [12]);
    assert.equal(
      escaped.includes("Escape"),
      false,
      "cancel stays within the page field",
    );
    for (const invalid of ["", "abc", "1.5", "2e1"]) {
      await type(window, invalid);
      await key(window, "Enter");
      assert.equal(field().value, "12");
      assert.deepEqual(changes, [12]);
    }
    await type(window, "9999");
    await key(window, "Enter");
    assert.equal(field().value, "22");
    await type(window, "0");
    await key(window, "Enter");
    assert.equal(field().value, "1");
    assert.deepEqual(changes, [12, 22, 1]);
    await render(React.createElement(Example, { disabled: true }));
    assert.equal(field().disabled, true);
    await key(window, "Enter");
    assert.deepEqual(changes, [12, 22, 1]);
  });
});

test("cursor-backed collections keep their honest sequential navigation", async () => {
  await withDOM(async ({ render }) => {
    const { CollectionTable } = await loadComponent(
      "src/design-system/collection-table.tsx",
    );
    let reads = 0;
    await render(
      React.createElement(CollectionTable, {
        ariaLabel: "Cursor records",
        testId: "cursor-records",
        rows: records.slice(0, 25),
        columns: [{ id: "label", label: "Name", width: 240, minWidth: 100, isRowHeader: true }],
        rowId: (row) => row.id,
        rowLabel: (row) => row.label,
        rowTestId: (row) => `record-${row.id}`,
        renderCell: (row) => row.label,
        emptyState: "Empty",
        remotePagination: {
          hasMore: true,
          loading: false,
          onNextPage: () => reads++,
        },
      }),
    );
    assert.equal(
      field(),
      null,
      "unknown remote pages are not offered as direct jumps",
    );
    await React.act(async () => button("Next page").click());
    assert.equal(reads, 1);
    assert.deepEqual(
      rowIds(),
      records.slice(0, 25).map((row) => row.id),
    );
  });
});

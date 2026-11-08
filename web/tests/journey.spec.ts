import { test, expect, type Page } from "@playwright/test";
import path from "node:path";
import { readFile } from "node:fs/promises";
test("empty installation → founder spending → reimbursement → export", async ({
  page,
  request,
}, info) => {
  expect(
    process.env.VULT_E2E_DISPOSABLE,
    "Explicitly identify a disposable installation",
  ).toBe("1");
  const initial = await (await request.get("/api/v1/books")).json();
  expect(
    initial.books,
    "Refuse to populate an installation with existing books",
  ).toHaveLength(0);
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await choose(page, "Book profile", "business");
  await page
    .getByLabel("Book name", { exact: true })
    .fill("Northstar Studio (synthetic)");
  await page
    .getByLabel("Founder display name", { exact: true })
    .fill("Alex Morgan");
  await page.getByLabel("Book start date", { exact: true }).fill("2026-01-01");
  await choose(page, "Accounting currency", "USD");
  await page.getByRole("button", { name: "Create Book", exact: true }).click();
  await totals(page, "0.00", "0.00", "0.00", "0.00");
  const filterTrigger = page.getByRole("button", {
    name: /^Filter transactions/,
  });
  const filters = page.getByRole("dialog", {
    name: "Transaction filters",
    exact: true,
  });
  await filterTrigger.click();
  await expect(filters).toBeVisible();
  await filterTrigger.click();
  await expect(filters).toBeHidden();
  async function entry(
    action: string,
    amount: string,
    description: string,
    date: string,
    select?: [string, string],
    receipt = false,
  ) {
    await openEntry(page, action);
    await page.getByLabel("Amount (USD)", { exact: true }).fill(amount);
    await page.getByLabel("Description", { exact: true }).fill(description);
    await page.getByLabel("Economic date", { exact: true }).fill(date);
    if (select) await choose(page, select[0], select[1]);
    if (receipt)
      await page
        .getByLabel("Receipt (optional)", { exact: true })
        .setInputFiles(
          path.join(import.meta.dirname, "fixtures/synthetic-hosting.pdf"),
        );
    await page.getByRole("button", { name: "Save entry", exact: true }).click();
    await expect(page.getByRole("dialog")).toBeHidden();
  }
  await entry(
    "Add founder funding",
    "250",
    "Initial founder capital",
    "2026-10-01",
    ["Funding type", "capital"],
  );
  await totals(page, "250.00", "0.00", "0.00", "0.00");
  await entry(
    "Add expense",
    "100",
    "Hosting, October",
    "2026-10-02",
    ["Paid by", "founder"],
    true,
  );
  await totals(page, "250.00", "100.00", "0.00", "100.00");
  await entry(
    "Record payment to founder",
    "40",
    "Partial hosting reimbursement",
    "2026-10-03",
    ["Payment purpose", "reimbursement"],
  );
  await totals(page, "210.00", "60.00", "0.00", "100.00");
  await entry("Add income", "200", "Website project", "2026-10-04");
  await totals(page, "410.00", "60.00", "200.00", "100.00");
  await entry("Add expense", "25", "Design software", "2026-10-05", [
    "Paid by",
    "business",
  ]);
  await totals(page, "385.00", "60.00", "200.00", "125.00");
  await page
    .getByRole("button", { name: "Hosting, October", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Underlying journal" }),
  ).toBeVisible();
  const download = page.waitForEvent("download");
  await page.getByRole("link", { name: /synthetic-hosting.pdf/ }).click();
  const file = await (await download).path();
  expect(await readFile(file!)).toEqual(
    await readFile(
      path.join(import.meta.dirname, "fixtures/synthetic-hosting.pdf"),
    ),
  );
  await page.getByRole("button", { name: "Close dialog", exact: true }).click();
  const csv = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "Manage transactions", exact: true })
    .click();
  await page.getByRole("menuitem", { name: "Export", exact: true }).click();
  await page
    .getByRole("menuitem", { name: "Transactions CSV", exact: true })
    .click();
  expect((await csv).suggestedFilename()).toBe("transactions.csv");
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await page.getByLabel("Search transactions in this Book").fill("Hosting");
  await expect(
    page.getByRole("button", { name: "Design software", exact: true }),
  ).toBeHidden();
  await page.getByLabel("Search transactions in this Book").fill("");
  await expect(
    page.getByRole("button", { name: "Design software", exact: true }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(
    390,
  );
  await openEntry(page, "Add expense");
  await expect(page.getByLabel("Amount (USD)")).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(
    page.getByRole("button", { name: "Manage transactions", exact: true }),
  ).toBeFocused();
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.screenshot({
    path: info.outputPath("transactions.png"),
    fullPage: true,
  });
  await page.reload();
  await totals(page, "385.00", "60.00", "200.00", "125.00");
  expect(errors).toEqual([]);
});
async function totals(
  page: Page,
  cash: string,
  payable: string,
  income: string,
  expenses: string,
) {
  const booksResponse = await page.request.get("/api/v1/books");
  expect(booksResponse.ok()).toBe(true);
  const { books } = await booksResponse.json();
  expect(books).toHaveLength(1);
  const stateResponse = await page.request.get(
    `/api/v1/state?book_id=${encodeURIComponent(books[0].id)}`,
  );
  expect(stateResponse.ok()).toBe(true);
  const state = await stateResponse.json();
  expect(state.book.id).toBe(books[0].id);
  expect(state.book.currency).toBe("USD");
  for (const [key, value] of [
    ["cash_minor", cash],
    ["reimbursements_minor", payable],
    ["income_minor", income],
    ["expenses_minor", expenses],
  ] as const)
    expect(state.summary[key]).toBe(BigInt(value.replace(".", "")).toString());
}
async function openEntry(page: Page, action: string) {
  const labels: Record<string, string> = {
    "Add expense": "Expense",
    "Add income": "Income",
    "Add founder funding": "Funding",
    "Record payment to founder": "Payment to founder",
  };
  await page
    .getByRole("button", { name: "Manage transactions", exact: true })
    .click();
  await page
    .getByRole("menuitem", { name: "New transaction", exact: true })
    .click();
  await page
    .getByRole("menuitem", { name: labels[action], exact: true })
    .click();
}
async function choose(page: Page, label: string, value: string) {
  const native = page.locator(
    `select[aria-hidden="true"][aria-label=${JSON.stringify(label)}]`,
  );
  const id = (await native.getAttribute("id"))!.replace(/-native$/, "");
  const option = await native
    .locator(`option[value=${JSON.stringify(value)}]`)
    .textContent();
  await page.locator(`button[id=${JSON.stringify(id)}]`).click();
  await page.getByRole("option", { name: option!, exact: true }).click();
}

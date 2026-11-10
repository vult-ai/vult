import { test, expect, type Page } from "@playwright/test";

// Runs against the browser preview server (see CONTRIBUTING.md): a synthetic
// owner with an empty Book and a business Book that has a year of entries and
// one cited source. Every value here is synthetic.

async function signIn(page: Page) {
  await page.goto("/__preview/sign-in");
  await expect(page.getByRole("button", { name: /Switch Book/ })).toBeVisible();
}

async function bookCode(page: Page, name: string) {
  const books = await page.evaluate(async () => {
    const r = await fetch("/api/v1/books");
    return (await r.json()) as {
      books: { id: string; name: string }[];
      addresses: { book_id: string; code: string }[];
    };
  });
  const book = books.books.find((b) => b.name === name);
  expect(book, `Book ${name}`).toBeTruthy();
  return books.addresses.find((a) => a.book_id === book!.id)!.code;
}

test("owner journey: create a Book, close a period, read sources and figures, sign out", async ({ page }) => {
  const errors: string[] = [];
  const failed: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("response", (r) => {
    if (r.url().includes("/api/") && r.status() >= 400 && r.status() !== 401) failed.push(`${r.status()} ${r.url()}`);
  });

  await signIn(page);
  // Each run creates its own Book, so the test can repeat on one server.
  const name = `Synthetic Household ${Date.now().toString(36)}`;

  // Create a Book from the Books page.
  const studio = await bookCode(page, "Northstar Studio (synthetic)");
  await page.goto(`/${studio}/books`);
  await page.getByRole("button", { name: "Create Book", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Create a Book" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("radio", { name: /Personal/ }).check();
  await dialog.getByLabel("Book name").fill(name);
  const currency = dialog.getByRole("combobox", { name: "Currency" });
  await currency.fill("Euro");
  const euro = page.getByRole("option", { name: /Euro \(EUR\)/ });
  await expect(euro).toBeVisible();
  // The list opens below its field rather than over its label.
  const field = await currency.boundingBox();
  const list = await euro.boundingBox();
  expect(list!.y).toBeGreaterThan(field!.y);
  await euro.click();
  await dialog.getByRole("button", { name: "Create Book", exact: true }).click();

  // It opens the new Book: a Personal euro Dashboard with nothing recorded yet.
  const household = await (async () => {
    await expect(page.getByRole("button", { name: `Switch Book, current Book ${name}` })).toBeVisible();
    return bookCode(page, name);
  })();
  await expect(page).toHaveURL(new RegExp(`/${household}$`));
  await expect(page.getByRole("heading", { name: "Income and spending" })).toBeVisible();
  // Amounts show by default; one click hides them, and one shows them again.
  await expect(page.getByRole("listitem", { name: /income €0\.00, expenses €0\.00/ }).first()).toBeAttached();
  await page.getByRole("button", { name: "Hide amounts" }).filter({ hasText: "Hide amounts" }).click();
  await expect(page.getByRole("listitem", { name: /income ••••••, expenses ••••••/ }).first()).toBeAttached();
  await page.getByRole("button", { name: "Show amounts" }).filter({ hasText: "Show amounts" }).click();
  await expect(page.getByRole("listitem", { name: /income €0\.00, expenses €0\.00/ }).first()).toBeAttached();

  // Close January 2026 in the new Book: the review names the year and,
  // with nothing recorded, finds nothing open.
  await page.goto(`/${household}/settings?tab=accounting`);
  await page.getByLabel("From").fill("2026-01-01");
  await page.getByLabel("Through").fill("2026-01-31");
  await page.getByRole("button", { name: "Review and close" }).click();
  const confirm = page.getByRole("dialog", { name: "Close this period" });
  await expect(confirm).toContainText("2026");
  await expect(confirm).toContainText("Nothing is open");
  await confirm.getByRole("button", { name: "Close period" }).click();
  await expect(confirm).toBeHidden();
  await expect(page.getByText("January 1, 2026 – January 31, 2026")).toBeVisible();
  await expect(page.getByRole("button", { name: "Reopen" })).toBeVisible();
  // Saving settings keeps the address on Settings.
  await expect(page).toHaveURL(new RegExp(`/${household}/settings`));

  // The business Book's source lists the entries that cite it.
  await page.goto(`/${studio}/sources`);
  await page.getByRole("button", { name: "Inspect synthetic-card-statement.pdf" }).click();
  const used = page.getByRole("region", { name: "Used by" });
  await expect(used.getByRole("listitem").first()).toBeVisible();
  expect(await used.getByRole("listitem").count()).toBeGreaterThan(3);

  // Its Dashboard for 2026 shows recorded income.
  await page.goto(`/${studio}?range=2026`);
  await expect(page.getByText("$57,750.00", { exact: true })).toBeVisible();

  // Sign out ends the session and says so plainly.
  await page.getByRole("button", { name: "Open profile menu" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page.getByText("You’re signed out")).toBeVisible();
  const after = await page.evaluate(async () => (await fetch("/api/v1/books")).status);
  expect(after).toBe(401);

  expect(errors, "page errors").toEqual([]);
  expect(failed, "failed API requests").toEqual([]);
});

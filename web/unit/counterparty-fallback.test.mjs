import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { counterpartyAvatarIdentity } from "../src/lib/counterparty-identity.ts";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const book = "10000000-0000-4000-8000-000000000001";
const otherBook = "10000000-0000-4000-8000-000000000002";
const contact = { id: "20000000-0000-4000-8000-000000000001", owner: "user", display_name: "Alex Example", revision: "1", photo_asset: null };
const google = { id: "google", name: "Google" };

test("unidentified or punctuation-only counterparties have a neutral minus instead of a person", async () => {
  await withDOM(async ({ render }) => {
    const { ContactAvatar } = await loadComponent("src/components/ui/contact-avatar.tsx");
    for (const name of ["", "   ", "***"]) {
      await render(React.createElement(ContactAvatar, { book, identityKey: "counterparty:unknown", name, base: "", brand: { state: "unknown" } }));
      const avatar = document.querySelector("[data-counterparty-avatar]");
      assert.equal(avatar.dataset.avatarUnknown, "true");
      assert.equal(avatar.dataset.tone, undefined);
      assert.equal(avatar.style.getPropertyValue("--account-avatar-color"), "var(--color-bg-tertiary)");
      assert.equal(avatar.querySelectorAll("svg").length, 1);
      assert.equal(avatar.querySelector("svg").children.length, 1);
      assert.equal(avatar.querySelector("svg circle"), null);
      assert.equal(avatar.textContent, "");
    }
  });
});


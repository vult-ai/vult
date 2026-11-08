import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";
const { createAccountSettingsStore, accountSettingsAPI, parseAccountSettings } = await loadComponent(
  "src/lib/account-settings.ts",
);
const id = "a".repeat(64),
  other = "b".repeat(64);
const defaults = () => ({
  user_id: id,
  revision: "0",
  profile: { version: 1, display_name: "", photo: null },
  preferences: {
    version: 1,
    language: "en",
    regional_format: "en-US",
    date_format: "standard",
    time_format: "24h",
    digit_grouping: true,
    negative_style: "minus",
  },
  appearance: "system",
});
const wait = () => new Promise((r) => setTimeout(r, 5));
async function settled(store) {
  for (let i = 0; i < 100 && store.getSnapshot().status === "saving"; i++)
    await wait();
  assert.notEqual(store.getSnapshot().status, "saving");
}
function backend() {
  let value = defaults();
  const writes = [];
  return {
    writes,
    get value() {
      return value;
    },
    api: {
      load: async () => structuredClone(value),
      save: async (p) => {
        writes.push(p);
        value = {
          ...value,
          ...(p.profile ? { profile: { ...value.profile, ...p.profile } } : {}),
          ...(p.preferences
            ? { preferences: { ...value.preferences, ...p.preferences } }
            : {}),
          ...(p.appearance ? { appearance: p.appearance } : {}),
          revision: String(Number(value.revision) + 1),
        };
        return structuredClone(value);
      },
    },
  };
}
test("profile, appearance and formats persist for independent clients without Book selectors", async () => {
  const b = backend(),
    a = createAccountSettingsStore(id, b.api, undefined, 0),
    device = createAccountSettingsStore(id, b.api, undefined, 0);
  try {
    await a.load();
    await device.load();
    a.update({
      profile: { display_name: "Taylor" },
      appearance: "dark",
      preferences: { regional_format: "pl-PL", date_format: "iso" },
    });
    await settled(a);
    await device.refresh();
    assert.equal(device.getSnapshot().value.profile.display_name, "Taylor");
    assert.equal(device.getSnapshot().value.appearance, "dark");
    assert.equal(
      device.getSnapshot().value.preferences.regional_format,
      "pl-PL",
    );
    assert.equal(b.writes[0].expected_revision, "0");
    assert.equal("book_id" in b.writes[0], false);
    assert.throws(() => a.update({ book_id: "spoof" }));
  } finally {
    a.dispose();
    device.dispose();
  }
});
test("edits during an in-flight save retain the latest draft and acknowledged revision", async () => {
  let finish;
  let value = defaults();
  const calls = [];
  const store = createAccountSettingsStore(
    id,
    {
      load: async () => value,
      save: (p) => {
        calls.push(p);
        if (calls.length === 1)
          return new Promise(
            (r) =>
              (finish = () => {
                value = {
                  ...value,
                  profile: { ...value.profile, ...p.profile },
                  revision: "1",
                };
                r(value);
              }),
          );
        value = {
          ...value,
          profile: { ...value.profile, ...p.profile },
          revision: "2",
        };
        return Promise.resolve(value);
      },
    },
    undefined,
    0,
  );
  try {
    await store.load();
    store.update({ profile: { display_name: "First" } });
    await wait();
    store.update({ profile: { display_name: "Final" } });
    finish();
    await settled(store);
    assert.equal(store.getSnapshot().value.profile.display_name, "Final");
    assert.equal(calls.length, 2);
    assert.equal(calls[1].expected_revision, "1");
  } finally {
    store.dispose();
  }
});
test("failed saves retain the draft for retry; disposal refuses late responses", async () => {
  let fail = true,
    finish;
  const store = createAccountSettingsStore(
    id,
    {
      load: async () => defaults(),
      save: async (p) => {
        if (fail) throw Error("offline");
        return {
          ...defaults(),
          revision: "1",
          profile: { ...defaults().profile, ...p.profile },
        };
      },
    },
    undefined,
    0,
  );
  try {
    await store.load();
    store.update({ profile: { display_name: "Retained" } });
    await settled(store);
    assert.equal(store.getSnapshot().status, "error");
    assert.equal(store.getSnapshot().value.profile.display_name, "Retained");
    fail = false;
    await store.retry();
    assert.equal(store.getSnapshot().status, "saved");
  } finally {
    store.dispose();
  }
  const late = createAccountSettingsStore(id, {
    load: () => new Promise((r) => (finish = r)),
    save: async () => defaults(),
  });
  const loading = late.load();
  late.dispose();
  finish({
    ...defaults(),
    profile: { version: 1, display_name: "Too late", photo: null },
  });
  await loading;
  assert.equal(late.getSnapshot().value.profile.display_name, "");
});
test("User mismatch clears identity; transport retains its original session CSRF; conflicts need reload", async () => {
  let denied = 0;
  const store = createAccountSettingsStore(
    id,
    {
      load: async () => ({ ...defaults(), user_id: other }),
      save: async () => defaults(),
    },
    () => denied++,
  );
  await store.load();
  assert.equal(denied, 1);
  assert.equal(store.getSnapshot().status, "signed-out");
  store.dispose();
  const previous = globalThis.fetch;
  let seen;
  try {
    globalThis.fetch = async (url, options) => {
      seen = { url, options };
      return {
        ok: false,
        status: 409,
        json: async () => ({ error: "changed on another device" }),
      };
    };
    const api = accountSettingsAPI("bound-session-csrf");
    const conflict = createAccountSettingsStore(
      id,
      { load: async () => defaults(), save: api.save },
      undefined,
      0,
    );
    await conflict.load();
    conflict.update({ appearance: "dark" });
    await settled(conflict);
    assert.equal(conflict.getSnapshot().status, "conflict");
    assert.equal(seen.url, "/api/v1/me/settings");
    assert.equal(seen.options.headers["X-CSRF-Token"], "bound-session-csrf");
    await conflict.retry();
    assert.equal(conflict.getSnapshot().value.appearance, "system");
    assert.equal(conflict.getSnapshot().status, "saved");
    conflict.dispose();
  } finally {
    globalThis.fetch = previous;
  }
});

test("private photo references resolve only to the current-origin User route", () => {
  const asset = { id: "12345678-1234-4234-9234-123456789abc", sha256: "c".repeat(64), content_type: "image/jpeg", size: 2048 };
  const payload = () => ({ ...defaults(), profile: { version: 1, display_name: "Private person", photo: null, photo_asset: { ...asset } } });
  assert.equal(parseAccountSettings(payload(), id).profile.photo, `/api/v1/me/avatar/${asset.id}?revision=${asset.sha256}`);
  assert.throws(() => parseAccountSettings({ ...payload(), user_id: other }, id));
  for (const change of [{id:"../another-user"}, {sha256:"not-a-hash"}, {content_type:"image/svg+xml"}, {size:147457}, {provider_url:"https://private-provider.test/image"}]) {
    const value=payload(); Object.assign(value.profile.photo_asset,change);
    assert.throws(() => parseAccountSettings(value,id));
  }
  const ambiguous=payload();ambiguous.profile.photo="https://private-provider.test/image";
  assert.throws(() => parseAccountSettings(ambiguous,id));
});

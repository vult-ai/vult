import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";
const {
  createProfileStore,
  profileStorageKey,
  parseUserProfile,
  profileInitials,
  inspectProfilePhoto,
  readProfilePhoto,
} = await loadComponent("src/lib/user-profile.ts");
const photo = "data:image/jpeg;base64,/9j/2Q==";
const memory = () => {
  const data = new Map();
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => data.set(k, v),
  };
};

test("personal profiles are independent of Books, browser fallback and other users", () => {
  const storage = memory();
  const a = { kind: "user", id: "synthetic-a" },
    b = { kind: "user", id: "synthetic-b" };
  const first = createProfileStore(a, storage),
    other = createProfileStore(b, storage);
  first.update({ display_name: "Taylor Example", photo });
  assert.equal(other.getSnapshot().value.display_name, "");
  assert.equal(
    createProfileStore({ kind: "browser" }, storage).getSnapshot().value.photo,
    null,
  );
  assert.equal(createProfileStore(a, storage).getSnapshot().value.photo, photo);
  assert.equal(profileInitials("  Taylor Example  "), "TE");
  assert.equal(profileInitials("Éva Example"), "ÉE");
  assert.equal(profileInitials(""), "");
  assert.throws(() => first.update({ book_id: "unrelated" }), /Unsupported/);
  assert.throws(
    () => first.update({ photo: "https://tracker.example.test/photo" }),
    /Unsupported/,
  );
  assert.throws(
    () => first.update({ display_name: "a".repeat(81) }),
    /Unsupported/,
  );
  assert.equal(
    parseUserProfile({
      version: 1,
      display_name: "Name",
      photo: "data:image/svg+xml;base64,PHN2Zz4=",
    }),
    null,
  );
  assert.ok(Object.isFrozen(first.getSnapshot().value));
});

test("corrupt, foreign-owner and newer profile data remains untouched", () => {
  const owner = { kind: "user", id: "synthetic-a" };
  for (const raw of [
    "{broken",
    JSON.stringify({
      owner,
      profile: { version: 2, display_name: "Future", photo: null },
    }),
    JSON.stringify({
      owner: { kind: "user", id: "other" },
      profile: { version: 1, display_name: "Other person", photo },
    }),
  ]) {
    const storage = memory();
    const key = profileStorageKey(owner);
    storage.setItem(key, raw);
    const store = createProfileStore(owner, storage);
    assert.equal(store.getSnapshot().value.display_name, "");
    assert.equal(store.getSnapshot().persistence, "invalid");
    store.update({ display_name: "Local only" });
    assert.equal(storage.getItem(key), raw);
  }
  const storage = memory(),
    store = createProfileStore(owner, storage);
  const future = JSON.stringify({
    owner,
    profile: { version: 2, display_name: "Future", photo: null },
  });
  storage.setItem(store.key, future);
  store.update({ display_name: "Local only" });
  assert.equal(storage.getItem(store.key), future);
  assert.equal(store.getSnapshot().persistence, "invalid");
});

test("storage errors stay truthful and a later successful choice can recover", () => {
  let failing = true;
  const storage = memory();
  const disk = {
    getItem: storage.getItem,
    setItem(k, v) {
      if (failing) throw Error("quota");
      storage.setItem(k, v);
    },
  };
  const store = createProfileStore({ kind: "browser" }, disk);
  store.update({ display_name: "Taylor" });
  assert.equal(store.getSnapshot().persistence, "unavailable");
  failing = false;
  store.update({ photo });
  assert.equal(store.getSnapshot().persistence, "saved");
  assert.equal(
    createProfileStore({ kind: "browser" }, storage).getSnapshot().value
      .display_name,
    "Taylor",
  );
  const unavailable = createProfileStore({ kind: "browser" }, null);
  unavailable.update({ display_name: "Here only" });
  assert.equal(unavailable.getSnapshot().persistence, "unavailable");
});

test("photo headers are bounded before decoding and reject SVG, huge dimensions and broken JPEG segments", async () => {
  const png = new Uint8Array(24);
  png.set([137, 80, 78, 71, 13, 10, 26, 10]);
  png.set([73, 72, 68, 82], 12);
  const view = new DataView(png.buffer);
  view.setUint32(16, 640);
  view.setUint32(20, 480);
  assert.deepEqual(inspectProfilePhoto(png), {
    width: 640,
    height: 480,
    mime: "image/png",
  });
  view.setUint32(16, 100000);
  assert.throws(() => inspectProfilePhoto(png), /megapixels/);
  const jpeg = Uint8Array.from([
    255, 216, 255, 192, 0, 8, 8, 0, 100, 0, 200, 1,
  ]);
  assert.deepEqual(inspectProfilePhoto(jpeg), {
    width: 200,
    height: 100,
    mime: "image/jpeg",
  });
  assert.throws(
    () => inspectProfilePhoto(Uint8Array.from([255, 216, 255, 192, 255, 255])),
    /valid PNG/,
  );
  assert.throws(
    () =>
      inspectProfilePhoto(
        new TextEncoder().encode('<svg xmlns="http://www.w3.org/2000/svg"/>'),
      ),
    /valid PNG/,
  );
  let read = false;
  await assert.rejects(
    readProfilePhoto({
      size: 2097153,
      arrayBuffer() {
        read = true;
        throw Error("must not read");
      },
    }),
    /up to 2 MB/,
  );
  assert.equal(read, false);
});

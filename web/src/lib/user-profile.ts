import {
  browserScopeKey,
  type PreferenceScope,
  type PreferenceStorage,
  type PreferencePersistence,
} from "./user-preferences";

export interface UserProfile {
  version: 1;
  display_name: string;
  photo: string | null;
}
export const EMPTY_USER_PROFILE: Readonly<UserProfile> = Object.freeze({
  version: 1,
  display_name: "",
  photo: null,
});
const safePhoto = (value: unknown): value is string =>
  typeof value === "string" &&
  value.length <= 196608 &&
  (/^data:image\/jpeg;base64,\/9j\/[A-Za-z0-9+/]+={0,2}$/.test(value) ||
    /^\/api\/v1\/me\/avatar\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\?revision=[0-9a-f]{64}$/.test(value));
export function parseUserProfile(value: unknown): Readonly<UserProfile> | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  const v = value as Record<string, unknown>;
  if (
    Object.keys(v).length !== 3 ||
    v.version !== 1 ||
    typeof v.display_name !== "string" ||
    v.display_name.length > 80 ||
    (v.photo !== null && !safePhoto(v.photo))
  )
    return null;
  return Object.freeze({
    version: 1,
    display_name: v.display_name,
    photo: v.photo,
  });
}
export const profileStorageKey = (scope: PreferenceScope) =>
  `vult-profile:v1:${browserScopeKey(scope)}`;
export function profileInitials(name: string) {
  return name
    .trim()
    .split(/\s+/)
    .slice(0, 2)
    .map((word) => Array.from(word)[0]?.toUpperCase() || "")
    .join("");
}
export function createProfileStore(
  scope: PreferenceScope,
  storage: PreferenceStorage | null,
) {
  const key = profileStorageKey(scope);
  const owner: PreferenceScope =
    scope.kind === "user"
      ? { kind: "user", id: scope.id }
      : { kind: "browser" };
  const listeners = new Set<() => void>();
  type Snapshot = {
    value: Readonly<UserProfile>;
    persistence: PreferencePersistence;
  };
  const result = (
    value: Readonly<UserProfile>,
    persistence: PreferencePersistence,
  ): Readonly<Snapshot> => Object.freeze({ value, persistence });
  function read(): Readonly<Snapshot> {
    if (!storage) return result(EMPTY_USER_PROFILE, "unavailable");
    let raw: string | null;
    try {
      raw = storage.getItem(key);
    } catch {
      return result(EMPTY_USER_PROFILE, "unavailable");
    }
    if (raw === null) return result(EMPTY_USER_PROFILE, "saved");
    try {
      const data = JSON.parse(raw);
      if (
        !data ||
        typeof data !== "object" ||
        Object.keys(data).length !== 2 ||
        !data.owner ||
        data.owner.kind !== owner.kind ||
        (owner.kind === "user"
          ? data.owner.id !== owner.id || Object.keys(data.owner).length !== 2
          : Object.keys(data.owner).length !== 1)
      )
        return result(EMPTY_USER_PROFILE, "invalid");
      const value = parseUserProfile(data.profile);
      return value
        ? result(value, "saved")
        : result(EMPTY_USER_PROFILE, "invalid");
    } catch {
      return result(EMPTY_USER_PROFILE, "invalid");
    }
  }
  let snapshot = read();
  let canPersist = snapshot.persistence === "saved";
  const notify = () => {
    for (const listener of listeners) listener();
  };
  return {
    key,
    getSnapshot: () => snapshot,
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    reload() {
      snapshot = read();
      canPersist = snapshot.persistence === "saved";
      notify();
    },
    update(patch: Partial<Pick<UserProfile, "display_name" | "photo">>) {
      if (
        !patch ||
        typeof patch !== "object" ||
        Object.keys(patch).some((k) => k !== "display_name" && k !== "photo")
      )
        throw Error("Unsupported profile value.");
      let value = parseUserProfile({ ...snapshot.value, ...patch });
      if (!value) throw Error("Unsupported profile value.");
      let persistence = snapshot.persistence;
      if (canPersist && storage) {
        const latest = read();
        if (latest.persistence !== "saved") {
          persistence = latest.persistence;
          canPersist = false;
        } else {
          if (snapshot.persistence === "saved")
            value = parseUserProfile({ ...latest.value, ...patch })!;
          try {
            storage.setItem(key, JSON.stringify({ owner, profile: value }));
            persistence = "saved";
          } catch {
            persistence = "unavailable";
          }
        }
      }
      snapshot = result(value, persistence);
      notify();
    },
  };
}

const maxUpload = 2 * 1024 * 1024;
function dimensions(width: number, height: number) {
  if (
    width < 1 ||
    height < 1 ||
    width > 8192 ||
    height > 8192 ||
    width * height > 16777216
  )
    throw Error("Choose a photo no larger than 16 megapixels.");
  return { width, height };
}
/** Inspect bounded encoded bytes before allocating a decoded image. */
export function inspectProfilePhoto(bytes: Uint8Array) {
  if (bytes.length > maxUpload) throw Error("Choose a PNG or JPEG up to 2 MB.");
  if (
    bytes.length >= 24 &&
    [137, 80, 78, 71, 13, 10, 26, 10].every((n, i) => bytes[i] === n) &&
    String.fromCharCode(...bytes.slice(12, 16)) === "IHDR"
  ) {
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    return {
      ...dimensions(view.getUint32(16), view.getUint32(20)),
      mime: "image/png",
    };
  }
  if (bytes[0] === 255 && bytes[1] === 216) {
    let offset = 2;
    while (offset + 3 < bytes.length) {
      if (bytes[offset++] !== 255) break;
      while (bytes[offset] === 255) offset++;
      const marker = bytes[offset++];
      if (marker === 217 || marker === 218) break;
      const length = (bytes[offset] << 8) | bytes[offset + 1];
      if (length < 2 || offset + length > bytes.length) break;
      if (
        [
          192, 193, 194, 195, 197, 198, 199, 201, 202, 203, 205, 206, 207,
        ].includes(marker) &&
        length >= 8
      )
        return {
          ...dimensions(
            (bytes[offset + 5] << 8) | bytes[offset + 6],
            (bytes[offset + 3] << 8) | bytes[offset + 4],
          ),
          mime: "image/jpeg",
        };
      offset += length;
    }
  }
  throw Error("Choose a valid PNG or JPEG photo.");
}
export async function readProfilePhoto(file: File) {
  if (file.size > maxUpload) throw Error("Choose a PNG or JPEG up to 2 MB.");
  const bytes = new Uint8Array(await file.arrayBuffer());
  const info = inspectProfilePhoto(bytes);
  if (file.type && file.type !== info.mime)
    throw Error("Choose a valid PNG or JPEG photo.");
  const url = await new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(Error("This photo could not be read."));
    reader.readAsDataURL(new Blob([bytes], { type: info.mime }));
  });
  const image = new Image();
  try {
    await new Promise<void>((resolve, reject) => {
      image.onload = () => resolve();
      image.onerror = () => reject(Error("This photo could not be read."));
      image.src = url;
    });
    dimensions(image.naturalWidth, image.naturalHeight);
    const canvas = document.createElement("canvas");
    canvas.width = 256;
    canvas.height = 256;
    const context = canvas.getContext("2d");
    if (!context) throw Error("This photo could not be prepared.");
    context.fillStyle = "#fff";
    context.fillRect(0, 0, 256, 256);
    const side = Math.min(image.naturalWidth, image.naturalHeight);
    context.drawImage(
      image,
      (image.naturalWidth - side) / 2,
      (image.naturalHeight - side) / 2,
      side,
      side,
      0,
      0,
      256,
      256,
    );
    const photo = canvas.toDataURL("image/jpeg", 0.85);
    if (!safePhoto(photo))
      throw Error("This photo could not be prepared. Try a smaller photo.");
    return photo;
  } finally {
    image.onload = null;
    image.onerror = null;
    image.src = "";
  }
}

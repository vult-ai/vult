import { ACCOUNT_AVATAR_STANDARD } from "../design-system/account-avatar-standard.ts";

/** Fit all painted pixels inside the centered circle, preserving the artwork.
 * Pixel outer corners are included, so antialiasing cannot consume clear space.
 * This is one shape-based rule for every brand, never an optical size override.
 */
export function circularMarkFit(
  pixels: Uint8Array | Uint8ClampedArray,
  width: number,
  height: number,
) {
  if (
    !Number.isSafeInteger(width) ||
    !Number.isSafeInteger(height) ||
    width < 1 ||
    height < 1 ||
    pixels.length !== width * height * 4
  )
    throw new Error("A complete mark raster is required.");
  let radiusSquared = 0;
  let left = width,
    right = -1,
    top = height,
    bottom = -1;
  for (let y = 0; y < height; y++)
    for (let x = 0; x < width; x++)
      if (pixels[(y * width + x) * 4 + 3]) {
        left = Math.min(left, x);
        right = Math.max(right, x);
        top = Math.min(top, y);
        bottom = Math.max(bottom, y);
      }
  if (right < 0) throw new Error("The mark has no visible artwork.");
  const centerX = (left + right + 1) / 2;
  const centerY = (top + bottom + 1) / 2;
  for (let y = 0; y < height; y++)
    for (let x = 0; x < width; x++) {
      if (!pixels[(y * width + x) * 4 + 3]) continue;
      const dx = Math.abs(x + 0.5 - centerX) + 0.5;
      const dy = Math.abs(y + 0.5 - centerY) + 0.5;
      radiusSquared = Math.max(radiusSquared, dx * dx + dy * dy);
    }
  if (!radiusSquared) throw new Error("The mark has no visible artwork.");
  const radiusRatio = Math.sqrt(radiusSquared) / (Math.max(width, height) / 2);
  const scale =
    Math.floor(Math.min(32, 1 / radiusRatio) * 1_000_000) / 1_000_000;
  return {
    scale,
    radiusRatio,
    offsetX: ((width / 2 - centerX) / width) * 100,
    offsetY: ((height / 2 - centerY) / height) * 100,
  };
}

const fits = new Map<string, ReturnType<typeof circularMarkFit>>();
const fitStorageKey = "vult:public-avatar-fit:512:v2";
let restoredFits = false;
function publicOriginal(source: string) {
  return /^(?:https:\/\/[a-z0-9.-]+|\/assets\/shared)\/sha256\/[a-f0-9]{64}\.(svg|png|webp)$/.test(source);
}
function validFit(value: ReturnType<typeof circularMarkFit>) {
  return value && [value.scale,value.radiusRatio,value.offsetX,value.offsetY].every(Number.isFinite) &&
    value.radiusRatio > 0 && value.radiusRatio <= Math.SQRT2 &&
    value.scale === Math.floor(Math.min(32, 1 / value.radiusRatio) * 1_000_000) / 1_000_000 &&
    Math.abs(value.offsetX) <= 50 && Math.abs(value.offsetY) <= 50;
}

/** Public, content-addressed fit results survive refresh without private IDs. */
export function cachedMarkFit(source: string) {
  if (!restoredFits && typeof window !== "undefined") {
    restoredFits = true;
    try {
      const raw = window.sessionStorage.getItem(fitStorageKey);
      if (raw && raw.length < 131_072) {
        const stored = JSON.parse(raw);
        if (Array.isArray(stored) && stored.length <= 200)
          for (const item of stored)
            if (Array.isArray(item) && publicOriginal(item[0]) && validFit(item[1])) fits.set(item[0],item[1]);
      }
    } catch { /* An unavailable cache never bypasses measurement. */ }
  }
  return fits.get(source);
}

const decoding = new Map<string, Promise<void>>();
/** Only previously measured, visible originals are warmed. No private identity
 * or new artwork is cached, and the caller caps this optional decode wait. */
export function warmCachedMark(source: string) {
  if (!cachedMarkFit(source) || typeof document === "undefined") return undefined;
  const existing = decoding.get(source);
  if (existing) return existing;
  const image = document.createElement("img");
  image.crossOrigin = "anonymous";
  image.referrerPolicy = "no-referrer";
  const ready = new Promise<void>((resolve) => {
    image.onerror = () => resolve();
    image.onload = async () => {
      try { if (image.decode) await image.decode(); } catch { /* Keep normal load/error handling. */ }
      resolve();
    };
    image.src = source;
  });
  decoding.set(source, ready);
  void ready.then(() => decoding.delete(source));
  return ready;
}

/** Inspect already-loaded anonymous artwork once per original URL. The canvas
 * stays detached, is discarded after measurement, and creates no asset variant.
 */
export function measureLoadedMark(image: HTMLImageElement, source: string) {
  const cached = cachedMarkFit(source);
  if (cached) return cached;
  if (!image.naturalWidth || !image.naturalHeight)
    throw new Error("The original mark is not loaded.");
  const size = ACCOUNT_AVATAR_STANDARD.measurementSize;
  const canvas = image.ownerDocument.createElement("canvas");
  canvas.width = canvas.height = size;
  const context = canvas.getContext("2d", { willReadFrequently: true });
  if (!context) throw new Error("The mark cannot be measured.");
  const contain = size / Math.max(image.naturalWidth, image.naturalHeight);
  const width = image.naturalWidth * contain;
  const height = image.naturalHeight * contain;
  context.drawImage(
    image,
    (size - width) / 2,
    (size - height) / 2,
    width,
    height,
  );
  const fit = circularMarkFit(
    context.getImageData(0, 0, size, size).data,
    size,
    size,
  );
  fits.set(source, fit);
  while (fits.size > 200) fits.delete(fits.keys().next().value!);
  try {
    window.sessionStorage.setItem(fitStorageKey,JSON.stringify([...fits].filter(([key])=>publicOriginal(key))));
  } catch { /* Measurement works when browser storage is disabled. */ }
  return fit;
}

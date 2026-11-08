import sharp from "sharp";
import { circularMarkFit } from "../../src/lib/circular-mark-fit.ts";
import { validateSVGMark } from "./svg-mark-admission.mjs";

/** Artwork admission v2. Inspect originals in memory; create no asset derivatives. */
export async function validateBrandArtwork(bytes, mediaType) {
  if (mediaType === "image/svg+xml") return validateSVGMark(bytes);
  if (!["image/png", "image/webp"].includes(mediaType))
    throw Error("Unsupported artwork media type");
  if (bytes.length < 20 || bytes.length > 262144)
    throw Error("Artwork must be at most256KiB");
  const decoder = sharp(bytes, {
    limitInputPixels: 4096 * 4096,
    failOn: "error",
  });
  const metadata = await decoder.metadata();
  if (`image/${metadata.format}` !== mediaType)
    throw Error("Invalid artwork media type");
  if ((metadata.pages || 1) !== 1)
    throw Error("Animated logos are not supported");
  if (
    !metadata.width ||
    !metadata.height ||
    metadata.width > 4096 ||
    metadata.height > 4096
  )
    throw Error("Artwork dimensions exceed4096px");
  const { data, info } = await decoder
    .toColourspace("srgb")
    .ensureAlpha()
    .resize(256, 256, {
      fit: "contain",
      background: { r: 0, g: 0, b: 0, alpha: 0 },
    })
    .raw()
    .toBuffer({ resolveWithObject: true });
  let painted = 0;
  const colors = new Set();
  for (let i = 3; i < data.length; i += 4)
    if (data[i]) {
      painted++;
      if (colors.size < 2)
        colors.add(`${data[i - 3]},${data[i - 2]},${data[i - 1]}`);
    }
  if (!painted) throw Error("Artwork cannot be decoded or is empty");
  if (painted / (info.width * info.height) >= 0.995 && colors.size < 2)
    throw Error("Artwork contains only an opaque empty canvas");
  return {
    circularFit: circularMarkFit(data, info.width, info.height),
    width: metadata.width,
    height: metadata.height,
    paintedCoverage: painted / (info.width * info.height),
  };
}

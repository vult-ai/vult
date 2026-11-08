import { JSDOM } from "jsdom";
import { Resvg } from "@resvg/resvg-js";
import { circularMarkFit } from "../../src/lib/circular-mark-fit.ts";

// Asset admission profile for docs/design/avatars.md. These checks
// inspect unchanged source bytes; rasterized pixels are discarded, never assets.
const tags = new Set([
  "svg",
  "path",
  "g",
  "defs",
  "clipPath",
  "rect",
  "circle",
  "ellipse",
  "polygon",
  "polyline",
  "line",
  "style",
  "title",
  "desc",
  "linearGradient",
  "radialGradient",
  "stop",
  "symbol",
  "use",
  "mask",
]);
const sampleSize = 256;

function checkColorsOnly(css) {
  const dom = new JSDOM("<style></style>");
  try {
    dom.window.document.querySelector("style").textContent = css;
    const themes = { light: "", dark: "" };
    function check(rules, theme) {
      for (const rule of rules) {
        if (
          !theme &&
          rule.type === 4 &&
          /^\(prefers-color-scheme:\s*(?:light|dark)\)$/.test(
            rule.conditionText,
          )
        ) {
          check(
            rule.cssRules,
            rule.conditionText.includes("dark") ? "dark" : "light",
          );
        } else if (rule.type === 1) {
          for (const property of Array.from(rule.style))
            if (!(
              (["fill", "stroke", "color"].includes(property) &&
                /^(?:#[a-f0-9]{3}(?:[a-f0-9]{3})?|rgb\([\d.,%\s]+\)|none|currentColor)$/i.test(
                  rule.style.getPropertyValue(property),
                )) ||
              (!theme &&
                property === "mask-type" && /^(?:luminance|alpha)$/.test(rule.style.getPropertyValue(property))) ||
              (!theme &&
                ["fill-rule", "clip-rule"].includes(property) &&
                /^(?:evenodd|nonzero)$/.test(
                  rule.style.getPropertyValue(property),
                )) ||
              (!theme &&
                property === "stroke-linejoin" &&
                /^(?:round|miter|bevel)$/.test(
                  rule.style.getPropertyValue(property),
                )) ||
              (!theme &&
                property === "stroke-linecap" &&
                /^(?:round|butt|square)$/.test(
                  rule.style.getPropertyValue(property),
                )) ||
              (!theme &&
                [
                  "stroke-width",
                  "stroke-miterlimit",
                  "opacity",
                  "fill-opacity",
                  "stroke-opacity",
                ].includes(property) &&
                /^[\d.]+$/.test(rule.style.getPropertyValue(property)))
            ))
              throw Error(
                "SVG styles may change opaque colors only; no backgrounds or theme geometry",
              );
          for (const selected of theme ? [theme] : ["light", "dark"])
            themes[selected] += rule.cssText;
        } else
          throw Error(
            "SVG styles must contain static colors or light/dark colors only",
          );
      }
    }
    check(dom.window.document.styleSheets[0].cssRules);
    return themes;
  } finally {
    dom.window.close();
  }
}

export function validateSVGMark(bytes) {
  if (bytes.length < 1 || bytes.length > 262144)
    throw Error("SVG mark must be at most 256 KiB");
  const svg = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  const markup = svg
    .replace(/<!--[^]*?-->/g, "")
    .replace(/^\s*<\?xml\s[^?]*\?>/, "")
    .replace(
      /<!DOCTYPE svg PUBLIC "-\/\/W3C\/\/DTD SVG 1\.1\/\/EN" "https?:\/\/www\.w3\.org\/Graphics\/SVG\/1\.1\/DTD\/svg11\.dtd"\s*>/,
      "",
    );
  if (/<!|<\?/.test(markup)) throw Error("SVG must be a static mark");
  const dom = new JSDOM(svg, { contentType: "image/svg+xml" });
  try {
    const document = dom.window.document;
    const root = document.documentElement;
    const elements = [...document.querySelectorAll("*")];
    const styles = [];
    if (
      root.localName !== "svg" ||
      elements.filter((el) => el.localName === "svg").length !== 1
    )
      throw Error("SVG must have one mark viewport");
    if (elements.length > 2048) throw Error("SVG mark is too complex");
    for (const element of elements) {
      if (
        !tags.has(element.localName) ||
        element.namespaceURI !== "http://www.w3.org/2000/svg"
      )
        throw Error(
          "SVG must contain only static vector shapes; embedded raster images are not marks",
        );
      for (const attribute of element.attributes) {
        if (
          /^on/i.test(attribute.localName) ||
          (/^(?:href|src)$/i.test(attribute.localName) &&
            !/^#[a-zA-Z0-9_-]+$/.test(attribute.value))
        )
          throw Error("SVG cannot contain active or external references");
        if (
          /url\s*\(/i.test(attribute.value) &&
          !/^url\(#[a-zA-Z0-9_-]+\)$/.test(attribute.value)
        )
          throw Error("SVG references must be local");
        if (/\bbackground\b|var\s*\(/i.test(attribute.value))
          throw Error(
            "SVG backgrounds belong to the shared renderer, not the artwork",
          );
        if (attribute.localName === "style")
          checkColorsOnly(`svg { ${attribute.value} }`);
      }
      if (element.localName === "style")
        styles.push({ element, themes: checkColorsOnly(element.textContent) });
    }
    let box = root
      .getAttribute("viewBox")
      ?.trim()
      .split(/[\s,]+/)
      .map(Number);
    // viewBox-only originals are valid SVGs. Do not rewrite them just to add
    // dimensions; their intrinsic ratio is the viewBox ratio.
    let width =
      root.hasAttribute("width") && !root.getAttribute("width").endsWith("%")
        ? Number(root.getAttribute("width").replace(/px$/, ""))
        : box?.[2];
    let height =
      root.hasAttribute("height") && !root.getAttribute("height").endsWith("%")
        ? Number(root.getAttribute("height").replace(/px$/, ""))
        : box?.[3];
    if (!box && Number.isFinite(width) && Number.isFinite(height)) box = [0, 0, width, height];
    if (box?.length === 4) {
      if (root.hasAttribute("width") && !root.hasAttribute("height")) height = width * box[3] / box[2];
      if (root.hasAttribute("height") && !root.hasAttribute("width")) width = height * box[2] / box[3];
    }
    if (
      !box ||
      box.length !== 4 ||
      !box.every(Number.isFinite) ||
      box[2] <= 0 ||
      box[3] <= 0 ||
      !Number.isFinite(width) ||
      !Number.isFinite(height) ||
      width <= 0 ||
      height <= 0 ||
      Math.max(width, height, ...box.map(Math.abs)) > 1000000 ||
      Math.abs(width / height - box[2] / box[3]) > 0.001 ||
      width / height < 0.03125 ||
      width / height > 32
    )
      throw Error(
        "SVG needs a bounded viewBox and proportional dimensions for a symbol",
      );

    let previousPixels;
    let metrics;
    for (const theme of ["light", "dark"]) {
      for (const { element, themes } of styles)
        element.textContent = themes[theme];
      const renderer = new Resvg(
        new dom.window.XMLSerializer().serializeToString(root),
        {
          fitTo: { mode: "zoom", value: sampleSize / Math.max(width, height) },
          font: { loadSystemFonts: false },
          // Omit background: the validation canvas must remain transparent.
        },
      );
      const image = renderer.render();
      const pixels = image.pixels;
      if (previousPixels)
        for (let i = 3; i < pixels.length; i += 4)
          if (pixels[i] !== previousPixels[i])
            throw Error(
              "SVG light/dark styles must preserve exactly the same mark silhouette",
            );
      previousPixels = pixels;
      let alpha = 0,
        painted = 0,
        left = image.width,
        top = image.height,
        right = -1,
        bottom = -1;
      for (let y = 0; y < image.height; y++)
        for (let x = 0; x < image.width; x++) {
          const value = pixels[(y * image.width + x) * 4 + 3];
          alpha += value;
          if (value > 8) {
            painted++;
            left = Math.min(left, x);
            right = Math.max(right, x);
            top = Math.min(top, y);
            bottom = Math.max(bottom, y);
          }
        }
      const coverage = alpha / (255 * image.width * image.height);
      const paintedCoverage = painted / (image.width * image.height);
      if (!coverage) throw Error("SVG mark must contain visible artwork");
      // A full circle covers 78.5% and an app tile approaches 100%. Reserve at
      // least 30% transparent canvas for this mark-only profile. This is not a
      // semantic trademark detector; official-source review is still required.
      const colors = new Set();
      for (let i = 0; i < pixels.length && colors.size < 2; i += 4)
        if (pixels[i + 3])
          colors.add(`${pixels[i]},${pixels[i + 1]},${pixels[i + 2]}`);
      if (paintedCoverage >= 0.995 && colors.size < 2)
        throw Error("SVG contains only an opaque empty canvas");
      const circularFit = circularMarkFit(pixels, image.width, image.height);
      if (
        !(circularFit.scale > 0 && circularFit.scale <= 32) ||
        circularFit.scale * circularFit.radiusRatio > 1
      )
        throw Error(
          "Painted artwork must fit the circular safe area and preserve radial clearance",
        );
      metrics = {
        circularFit,
        coverage,
        paintedCoverage,
        width: image.width,
        height: image.height,
        bounds: [left, top, right, bottom],
      };
    }
    return metrics;
  } finally {
    dom.window.close();
  }
}

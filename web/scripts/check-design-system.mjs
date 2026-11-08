// Vult Open owns its design system. Its rules are enforced by the standard
// checks (menus, feedback, typography, product terms, name links); this check
// guards the assets and page-level rules those do not cover.
import { readFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import path from "node:path";

const root = path.resolve(import.meta.dirname, "..");
const read = (file) => readFile(path.join(root, file));
const sha = (value) => createHash("sha256").update(value).digest("hex");

// The bundled Geist font is a licensed third-party file; it ships unmodified.
const font = {
  target: "src/design-system/fonts/Geist-Latin.woff2",
  sha256: "9b6f5ff45b278c744b5f379a2c4ecbaf858a842b8eaf82ac8d21b699ca16c608",
};
if (sha(await read(font.target)) !== font.sha256) throw Error("Geist font drift");

// The server's content security policy allows no inline script.
const html = (await read("index.html")).toString();
if (/<script(?![^>]*src=)[^>]*>\s*\S/.test(html))
  throw Error("Inline script conflicts with the server CSP");

const logo = (await read("public/brand/vult-logo.svg")).toString();
if ((logo.match(/fill="#007DF9"/g) || []).length !== 4)
  throw Error("Canonical logo color changed");

console.log("Verified the Geist font, logo and CSP bootstrap.");
await (await import("./check-date-format.mjs")).checkDateFormat();

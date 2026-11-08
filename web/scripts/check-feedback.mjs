import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Outcome notices, confirmations and pending buttons share one standard. The
// shared components may use the patterns they replace; nothing else may.
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../src");
const owners = new Set([
  "components/ui/notice.tsx",
  "components/ui/confirm-action.tsx",
]);

export function checkSource(file, source) {
  const failures = [];
  if (owners.has(file) || file.startsWith("design-system/catalog/")) return failures;
  if (/(?:&&|\?|:)\s*\(?\s*<(?:p|div|span)\b[^>]*\b(?:role\s*=\s*["']status["']|aria-live\s*=)/.test(source))
    failures.push("Use Notice for an outcome; a live region mounted with its text is often not read.");
  if (/className\s*=\s*["'](?:save-notice|notice)["']/.test(source))
    failures.push("Use Notice instead of a hand-built notice.");
  if (/(?:variant|color)\s*=\s*["'](?:(?:primary|secondary|tertiary|link)-)?destructive["']/.test(source))
    failures.push("Destructive actions are confirmed through ConfirmAction or ConfirmFooter.");
  for (const [, button] of source.matchAll(/<Button\b([\s\S]*?)<\/Button>/g))
    if (/\?\s*["'][A-Z][a-z]+ing…["']\s*:/.test(button)) {
      failures.push("Keep a button’s label while pending; pass busy or isLoading to show progress.");
      break;
    }
  for (const [, text] of source.matchAll(/<Notice\b[^>]*>([^{<]*)<\/Notice>/g)) {
    const copy = text.replace(/\s+/g, " ").trim();
    if (!/\.$/.test(copy) || copy.includes("!"))
      failures.push(`Notice copy is whole sentences ending with a full stop: “${copy}”.`);
  }
  return failures;
}

function scan(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((entry) =>
    entry.isDirectory()
      ? scan(path.join(dir, entry.name))
      : /\.[jt]sx?$/.test(entry.name)
        ? [path.join(dir, entry.name)]
        : [],
  );
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const failures = scan(root).flatMap((file) =>
    checkSource(path.relative(root, file), fs.readFileSync(file, "utf8")).map(
      (message) => `${path.relative(root, file)}: ${message}`,
    ),
  );
  if (failures.length) {
    process.stderr.write(failures.join("\n") + "\n");
    process.exitCode = 1;
  } else process.stdout.write("Feedback standard: PASS\n");
}

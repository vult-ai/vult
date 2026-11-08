import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
const root = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../src",
);
const feedback = new Set([
  "components/ui/error-notice.tsx",
  "components/ui/failure-state.tsx",
  "components/ui/empty-state.tsx",
  "components/ui/loading-state.tsx",
  "components/ui/state-layout.tsx",
  "components/ui/startup-error.ts",
]);
export function checkSource(file, source) {
  const failures = [];
  if (file === "lib/errors.ts" || file.startsWith("design-system/catalog/"))
    return failures;
  const integrityReaders = new Set([
    "lib/cash-summary.ts",
    "lib/v0-ledger.ts",
    "lib/transaction-journal.ts",
    "lib/account-detail-read.ts",
  ]);
  if (/\bverifiedReadFailure\b/.test(source) && !integrityReaders.has(file))
    failures.push(
      "Only reviewed integrity validators may create verified critical failures.",
    );
  if (
    !feedback.has(file) &&
    /<Error(?:Notice|Message)\b[^>]*\b(?:severity|critical|icon)\s*=|\bdata-severity\s*=/.test(
      source,
    )
  )
    failures.push(
      "Severity and icons come from the shared failure policy, never page overrides.",
    );
  if (
    /throw\s+new\s+(?:APIError|Error)\s*\(\s*(?:data|response|body|payload)\??\.error\b/.test(
      source,
    )
  )
    failures.push(
      "Translate API failures through lib/errors; server prose is not product copy.",
    );
  if (/\{\s*(?:data|response|body|payload)\??\.error\b[^}]*\}/.test(source)) failures.push("Render a normalized failure, not the API error body.");
  if (
    !feedback.has(file) &&
    (/role\s*=\s*["']alert["']|setAttribute\(["']role["'],\s*["']alert["']\)/.test(
      source,
    ) ||
      /className\s*=\s*["']error["']/.test(source))
  )
    failures.push(
      "Use FailureState, ErrorNotice or FieldError; do not create another error surface.",
    );
  if (
    !feedback.has(file) &&
    /role\s*=\s*["']status["'][^>]*>\s*(?:Loading|Opening|Reading|Checking|Preparing)\b[^<{]*…/.test(
      source,
    )
  )
    failures.push("Use LoadingState for waiting; do not hand-build a loading line.");
  if (/(?:\?|return)\s*\(?\s*(?:<div\b[^>]*>\s*)?<Error(?:Message|Notice)\b[^>]*\bintent=["']read["']/.test(source))
    failures.push("A read failure that replaces content is a FailureState; ErrorMessage sits beside content that still works.");
  if (/\bemptyState=\{\s*<p\b/.test(source))
    failures.push("Use EmptyState for an empty table or list.");
  if (feedback.has(file) && /<details\b|["']Technical details["']|failure\.(?:code|status)|error-notice-(?:title|retained)/.test(source)) failures.push("Keep error notices to one message; debugging details and repeated headings stay out of the UI.");
  return failures;
}
function scan(dir) {
  return fs
    .readdirSync(dir, { withFileTypes: true })
    .flatMap((entry) =>
      entry.isDirectory()
        ? scan(path.join(dir, entry.name))
        : /\.[jt]sx?$/.test(entry.name)
          ? [path.join(dir, entry.name)]
          : [],
    );
}
if (
  process.argv[1] &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  const failures = scan(root).flatMap((file) =>
    checkSource(path.relative(root, file), fs.readFileSync(file, "utf8")).map(
      (message) => `${path.relative(root, file)}: ${message}`,
    ),
  );
  if (failures.length) {
    process.stderr.write(failures.join("\n") + "\n");
    process.exitCode = 1;
  } else process.stdout.write("Error handling standard: PASS\n");
}

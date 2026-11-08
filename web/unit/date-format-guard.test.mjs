import assert from "node:assert/strict";
import test from "node:test";
import { dateFormatViolations } from "../scripts/check-date-format.mjs";

test("the date guard rejects independent date policies without rejecting numeric formatting or date inputs", () => {
  for (const code of [
    'new Intl.DateTimeFormat("en-US", { year: "2-digit" }).format(date)',
    'date.toLocaleDateString("en-US")',
    'useDateFormatter({ month: "short" })',
    'new Date(value).toLocaleString()',
  ]) assert.equal(dateFormatViolations(code).length, 1, code);
  assert.deepEqual(dateFormatViolations('formatDate(value); <RecordDate value={value} />; number.toLocaleString(); <input type="date" value={iso} />;'), []);
});

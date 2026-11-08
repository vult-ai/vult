import assert from "node:assert/strict";
import test from "node:test";
import { counterpartyIdentity } from "../src/lib/counterparty-identity.ts";

test("explicit counterparty links are stable across casing and spacing, with no partial brand guesses", () => {
  assert.equal(
    counterpartyIdentity(" Wise ").key,
    counterpartyIdentity("WISE").key,
  );
  assert.equal(counterpartyIdentity("Ｗｉｓｅ").institutionId, "wise");
  assert.equal(counterpartyIdentity("Mercury").institutionId, "mercury");
  for (const name of [
    "Wise interest",
    "Mercury Checking",
    "Otherwise",
    "__proto__",
    "toString",
    "Unknown brand",
  ])
    assert.equal(counterpartyIdentity(name).institutionId, undefined);
  assert.equal(
    counterpartyIdentity("  Alex   Example ").key,
    counterpartyIdentity("alex example").key,
  );
  for (const name of ["", " ", null, undefined]) {
    assert.equal(counterpartyIdentity(name).key, "counterparty:unknown");
    assert.equal(counterpartyIdentity(name).institutionId, undefined);
  }
});

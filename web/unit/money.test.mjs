import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";

test("currency symbols preserve every minor unit including large values, negatives and different scales", async () => {
  const {money}=await loadComponent("src/lib/api.ts");
  const usd={currency:"USD",scale:2};
  assert.equal(money("1057143",usd),"$10,571.43");
  assert.equal(money("9223372036854775807",usd),"$92,233,720,368,547,758.07");
  assert.equal(money("-1",usd),"−$0.01");
  assert.equal(money("0",usd),"$0.00");
  assert.equal(money("12345",{currency:"JPY",scale:0}),"¥12,345");
  assert.equal(money("12345",{currency:"EUR",scale:2}),"€123.45");
  assert.equal(money("12345",{currency:"GBP",scale:2}),"£123.45");
  assert.equal(money("12345",{currency:"KWD",scale:3}),"KWD\u00a012.345");
  assert.equal(money("12345",{currency:"CAD",scale:2}),"CA$123.45");
});

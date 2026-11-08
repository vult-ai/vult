import assert from "node:assert/strict";
import test from "node:test";
import { loadComponent } from "./helpers/dom.mjs";
const { resolveGridWidths: resolve, resizeGridColumn: resize, gridColumnBounds: bounds, gridWidthPreferences: commit, gridWidthTotal: total } = await loadComponent("src/components/statements/financial-grid-widths.ts");
const sizes = [
  { id: "account", min: 160, preferred: 320, max: 4096 },
  { id: "current", min: 139, preferred: 184, max: 4096 },
  { id: "prior", min: 139, preferred: 184, max: 4096 },
  { id: "variance", min: 128, preferred: 184, max: 4096 },
];
const vector = values => Object.fromEntries(sizes.map((size, index) => [size.id, values[index]]));
function independent(before, after, id) {
  for (const size of sizes) if (size.id !== id) assert.equal(after[size.id], before[size.id]);
  assert.equal(total(sizes, after)-total(sizes, before), after[id]-before[id]);
}
test("a bloated date column can shrink without bloating its neighbor, like Activity", () => {
  const before = vector([320, 982, 177, 147]), after = resize(sizes, before, 'current', 184);
  assert.deepEqual(after, vector([320,184,177,147])); independent(before,after,'current');
  const saved = commit(sizes,{},after);
  assert.deepEqual(resolve(sizes,saved,2000),after); assert.deepEqual(resolve(sizes,saved,600),after);
});
test("first resize freezes the resolved vector; no release, reload or viewport refit", () => {
  const before = resolve(sizes,{},1040), after = resize(sizes,before,'prior',200);
  assert.deepEqual(before,vector([488,184,184,184]));
  independent(before,after,'prior');
  const saved=commit(sizes,{},after);
  for(const viewport of [1040,600,2000]) assert.deepEqual(resolve(sizes,saved,viewport),after);
});
test("third-column, terminal and fractional widths are independent in both directions", () => {
  const before=Object.freeze(vector([319.25,198.5,177.75,2500]));
  for(const size of sizes)for(const delta of [-16,16]){
    const after=resize(sizes,before,size.id,before[size.id]+delta);independent(before,after,size.id);
    assert.equal(after[size.id],before[size.id]+delta);
    assert.deepEqual(resolve(sizes,commit(sizes,{},after),1040),after);
  }
  assert.equal(resize(sizes,before,'prior',before.prior),before);
});
test("reset changes only its column and bounds do not depend on neighboring widths",()=>{
  const before=vector([575,982,177,4096]), after=resize(sizes,before,'current',184);
  assert.equal(after.current,184);independent(before,after,'current');
  assert.deepEqual(bounds(sizes,before,'prior'),{min:139,max:4096});
  assert.equal(resize(sizes,before,'missing',200),before);
});
test("hidden widths survive the bounded preference cache",()=>{
  const hidden=Object.fromEntries(Array.from({length:40},(_,i)=>['month:'+i,190]));
  const current=vector([320,184,184,184]), saved=commit(sizes,hidden,current);
  assert.equal(Object.keys(saved).length,32);assert.equal(saved['month:39'],190);
  for(const size of sizes)assert.equal(saved[size.id],current[size.id]);
});
test("deterministic matrix preserves unrelated widths and exact manual reloads",()=>{
  let seed=721;const random=()=>{seed=(Math.imul(seed,1664525)+1013904223)>>>0;return seed/2**32;};
  for(let run=0;run<500;run++){
    const before=Object.fromEntries(sizes.map(size=>[size.id,size.min+Math.floor(random()*700)]));
    for(const size of sizes){const after=resize(sizes,before,size.id,before[size.id]+Math.floor((random()-.5)*1200));independent(before,after,size.id);assert.deepEqual(resolve(sizes,commit(sizes,{},after),500+random()*2000),after);}
  }
});

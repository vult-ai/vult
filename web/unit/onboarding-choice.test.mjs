import assert from "node:assert/strict";
import test from "node:test";
import {React,withDOM,loadComponent} from "./helpers/dom.mjs";

test("country aliases find one country while searches and cancellation preserve the committed identity", async () => {
 await withDOM(async ({render,window}) => {
  const {OnboardingChoice}=await loadComponent("src/components/onboarding/onboarding-choice.tsx");
  const {countryOption}=await loadComponent("src/components/onboarding/country-search.ts");
  const changes=[];
  function Country(){
   const [value,setValue]=React.useState("US");
   return React.createElement(OnboardingChoice,{label:"Registered in",name:"country",value,
    items:[...[{code:"US",name:"United States"},{code:"TR",name:"Türkiye"},{code:"GB",name:"United Kingdom"},{code:"UA",name:"Ukraine"}].map(countryOption),{id:"unregistered",label:"Not formally registered"},{id:"unknown",label:"Not sure yet"}],
    onChange:next=>{changes.push(next);setValue(next);}});
  }
  await render(React.createElement(Country));
  const input=document.querySelector('input[role="combobox"]');
  const committed=()=>document.querySelector('select[name="country"]').value;
  const options=()=>[...document.querySelectorAll('[role="option"]')].map(n=>n.textContent).filter(text=>!text.startsWith("Not "));
  const query=async text=>{await React.act(async()=>{Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype,"value").set.call(input,text);input.dispatchEvent(new window.Event("input",{bubbles:true}));});};
  await React.act(async()=>input.click());
  assert.equal(input.value,"");assert.equal(committed(),"US");assert.deepEqual(changes,[]);
  for(const text of ["us","usa","United States","United States of America"]){await query(text);assert.deepEqual(options(),["United States"]);assert.equal(committed(),"US");}
  await query("usa");assert.equal(document.querySelectorAll('[role="option"]').length,1);
  await query("uk");assert.deepEqual(options(),["United Kingdom"]);assert.equal(committed(),"US");
  await query("Turkey");assert.deepEqual(options(),["Türkiye"]);
  await React.act(async()=>input.blur());assert.equal(input.value,"United States");assert.equal(committed(),"US");assert.deepEqual(changes,[]);
  await React.act(async()=>input.focus());await query("usa");
  await React.act(async()=>document.querySelector('[role="option"]').click());
  assert.equal(input.value,"United States");assert.equal(committed(),"US");assert.deepEqual(changes,[]);
  await React.act(async()=>input.click());
  assert.equal(input.getAttribute("aria-expanded"),"true");
  assert.equal(input.value,"");
  await query("Turkey");
  await React.act(async()=>document.querySelector('[role="option"]').click());
  assert.deepEqual(changes,["TR"]);assert.equal(committed(),"TR");assert.equal(input.value,"Türkiye");
 });
});
test("exact-label search stays provisional on blur and unmatched searches retain explicit fallback choices",async()=>{
 await withDOM(async({render,window})=>{
  const {OnboardingChoice}=await loadComponent("src/components/onboarding/onboarding-choice.tsx");const changes=[];
  await render(React.createElement(OnboardingChoice,{label:"Country",value:"GB",items:[{id:"GB",label:"United Kingdom"},{id:"US",label:"United States"},{id:"custom",label:"Another jurisdiction"},{id:"unknown",label:"Not sure yet"}],onChange:value=>changes.push(value)}));
  const input=document.querySelector('input[role="combobox"]');
  await React.act(async()=>input.focus());
  await React.act(async()=>{Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype,"value").set.call(input,"United States");input.dispatchEvent(new window.Event("input",{bubbles:true}));});
  assert.deepEqual(changes,[]);await React.act(async()=>input.blur());assert.deepEqual(changes,[]);assert.equal(input.value,"United Kingdom");
  await React.act(async()=>input.focus());
  await React.act(async()=>{Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype,"value").set.call(input,"California");input.dispatchEvent(new window.Event("input",{bubbles:true}));});
  const options=[...document.querySelectorAll('[role="option"]')].map(node=>node.textContent);assert.ok(options.includes("Another jurisdiction"));assert.ok(options.includes("Not sure yet"));assert.ok(!options.includes("United States"));
 });
});

test("Delete and Backspace can edit a closed focused country without changing its committed value", async () => {
 await withDOM(async ({render,window})=>{
  const {OnboardingChoice}=await loadComponent("src/components/onboarding/onboarding-choice.tsx");
  const changes=[];
  await render(React.createElement(OnboardingChoice,{label:"Registered in",name:"country",value:"US",items:[{id:"US",label:"United States"},{id:"TR",label:"Türkiye"}],onChange:value=>changes.push(value)}));
  const input=document.querySelector('input[role="combobox"]');
  const edit=async text=>React.act(async()=>{Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype,"value").set.call(input,text);input.dispatchEvent(new window.Event("input",{bubbles:true}));});
  await React.act(async()=>input.focus());
  await React.act(async()=>input.dispatchEvent(new window.KeyboardEvent("keydown",{key:"Escape",code:"Escape",bubbles:true,cancelable:true})));
  assert.equal(input.getAttribute("aria-expanded"),"false");
  assert.equal(input.value,"United States");
  await edit("United State");assert.equal(input.value,"United State");
  await edit("");assert.equal(input.value,"");
  assert.equal(document.querySelector('select[name="country"]').value,"US");assert.deepEqual(changes,[]);
  await edit("Turkey");assert.equal(input.value,"Turkey");
  await React.act(async()=>input.blur());assert.equal(input.value,"United States");assert.deepEqual(changes,[]);
 });
});

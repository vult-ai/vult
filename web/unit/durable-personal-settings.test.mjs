import { phosphorPlugin } from "./helpers/phosphor-plugin.mjs";
import assert from "node:assert/strict";
import test from "node:test";
import { build } from "esbuild";
import { createRequire } from "node:module";
import { React,withDOM } from "./helpers/dom.mjs";
async function components(){
 const result=await build({stdin:{contents:'export {PersonalSettingsHost,usePersonalAccount} from "./components/shell/personal-settings-host";export {AppShell} from "./components/shell/app-shell";',resolveDir:new URL("../src",import.meta.url).pathname,loader:"tsx"},jsx:"automatic",bundle:true,write:false,platform:"node",format:"cjs",packages:"external",loader:{".css":"empty",".module.css":"empty"},plugins:[phosphorPlugin]});
 const module={exports:{}};new Function("module","exports","require",result.outputFiles[0].text)(module,module.exports,createRequire(import.meta.url));return module.exports;
}
const click=async node=>{assert.ok(node);await React.act(async()=>{node.click();await new Promise(requestAnimationFrame);});};
async function waitFor(predicate){for(let n=0;n<100;n++){if(predicate())return;await React.act(async()=>{await new Promise(r=>setTimeout(r,5));});}assert.ok(predicate());}
const button=label=>[...document.querySelectorAll("button")].find(node=>node.textContent.trim()===label);
test("local personal settings use the instance and import browser values only after review",async()=>{
 await withDOM(async({render,window})=>{
  const {PersonalSettingsHost,AppShell}=await components();const prior=globalThis.fetch,id="a".repeat(64);let writes=[];
  const browserProfile=JSON.stringify({owner:{kind:"browser"},profile:{version:1,display_name:"Browser person",photo:null}});
  window.localStorage.setItem("vult-profile:v1:browser",browserProfile);window.localStorage.setItem("vult-theme","dark");
  let saved={user_id:id,revision:"1",profile:{version:1,display_name:"Instance person",photo:null},preferences:{version:1,language:"en",regional_format:"en-US",date_format:"standard",time_format:"24h",digit_grouping:true,negative_style:"minus"},appearance:"light"};
  globalThis.fetch=async(url,options={})=>{
   if(String(url).endsWith("/session"))return{ok:true,json:async()=>({mode:"local",user_id:id,csrf:"instance-session"})};
   assert.equal(url,"/api/v1/me/settings");
   if(options.method==="POST"){
    const patch=JSON.parse(options.body);writes.push(patch);assert.equal(options.headers["X-CSRF-Token"],"instance-session");assert.equal(patch.expected_revision,saved.revision);assert.equal("book_id" in patch,false);assert.equal("user_id" in patch,false);
    saved={...saved,profile:{...saved.profile,...patch.profile},appearance:patch.appearance??saved.appearance,preferences:{...saved.preferences,...patch.preferences},revision:String(Number(saved.revision)+1)};
   }
   return{ok:true,json:async()=>structuredClone(saved)};
  };
  function Fixture(){const[book,setBook]=React.useState("Book A");return React.createElement(PersonalSettingsHost,null,React.createElement(AppShell,{bookName:book,bookSwitcher:React.createElement("button",{"aria-label":"Switch fixture Book",onClick:()=>setBook(book==="Book A"?"Book B":"Book A")},book),ready:true,active:"",navigation:[],onSettings(){}},React.createElement("input",{"aria-label":"Retained draft",defaultValue:"Keep this"})));}
  try{
   await render(React.createElement(Fixture,{key:"first"}));await waitFor(()=>document.documentElement.dataset.vultThemePreference==="light");
   const draft=document.querySelector('input[aria-label="Retained draft"]');
   await click(document.querySelector('button[aria-label="Open profile menu"]'));
   assert.match(document.body.textContent,/Instance person/);assert.doesNotMatch(document.body.textContent,/Browser person/);
   await click([...document.querySelectorAll('[role="menuitem"]')].find(node=>node.textContent==="Profile & preferences"));
   await waitFor(()=>button("Review browser copy"));
   assert.equal(document.querySelector('.personal-settings-dialog [role="status"]').textContent,"");assert.equal(writes.length,0);
   await click(button("Review browser copy"));assert.match(document.querySelector('[role="dialog"]').textContent,/Browser person/);assert.equal(writes.length,0);
   await click(button("Use browser copy"));await waitFor(()=>saved.profile.display_name==="Browser person"&&!button("Review browser copy"));
   assert.equal(writes.length,1);assert.equal(saved.appearance,"dark");assert.equal(window.localStorage.getItem("vult-profile:v1:browser"),browserProfile);
   await click(document.querySelector('button[aria-label="Close dialog"]'));await click(document.querySelector('button[aria-label="Switch fixture Book"]'));assert.equal(document.querySelector('input[aria-label="Retained draft"]'),draft);
   window.localStorage.clear();await render(React.createElement(Fixture,{key:"fresh-browser"}));await waitFor(()=>document.documentElement.dataset.vultThemePreference==="dark");
   await click(document.querySelector('button[aria-label="Open profile menu"]'));assert.match(document.body.textContent,/Browser person/);assert.equal(writes.length,1,"clearing browser values must not write defaults over the instance");
  }finally{globalThis.fetch=prior;}
 });
});

test("the verified session mode is shared and disappears when session refresh fails", async () => {
  await withDOM(async ({ render, unmount, window }) => {
    const { PersonalSettingsHost, usePersonalAccount } = await components();
    const before = globalThis.fetch;
    const user = "b".repeat(64);
    let failure = 0;
    let reads = 0;
    globalThis.fetch = async (url) => {
      if (url === "/auth/session") {
        reads += 1;
        if (failure) return { ok: false, status: failure, json: async () => ({}) };
        return { ok: true, json: async () => ({ mode: "local", user_id: user, csrf: "synthetic-session" }) };
      }
      assert.equal(url, "/api/v1/me/settings");
      return { ok: true, json: async () => ({ user_id: user, revision: "1", profile: { version: 1, display_name: "Synthetic owner", photo: null }, preferences: { version: 1, language: "en", regional_format: "en-US", date_format: "standard", time_format: "24h", digit_grouping: true, negative_style: "minus" }, appearance: "light" }) };
    };
    function Mode() {
      return React.createElement("output", { "data-mode": usePersonalAccount()?.mode ?? "unavailable" });
    }
    const mode = () => document.querySelector("output")?.dataset.mode;
    try {
      await render(React.createElement(PersonalSettingsHost, null, React.createElement(Mode)));
      await waitFor(() => mode() === "local");
      assert.equal(reads, 1, "all consumers share the host's existing session read");
      failure = 503;
      await React.act(async () => window.dispatchEvent(new Event("focus")));
      await waitFor(() => mode() === "unavailable");
      failure = 0;
      await React.act(async () => window.dispatchEvent(new Event("focus")));
      await waitFor(() => mode() === "local");
      failure = 401;
      await React.act(async () => window.dispatchEvent(new Event("focus")));
      await waitFor(() => mode() === "unavailable");
    } finally {
      await unmount();
      globalThis.fetch = before;
    }
  });
});

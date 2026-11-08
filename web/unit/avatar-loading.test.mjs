import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { React, loadComponent, withDOM } from "./helpers/dom.mjs";

const artwork = JSON.parse(await readFile(new URL("./fixtures/shared-assets/brand-logos.v1.json", import.meta.url), "utf8"));
const search = artwork.institutions.find(item => item.id === "example-search");
const book = { id:"10000000-0000-4000-8000-000000000001",name:"Synthetic",founder:"Preview",profile:"personal",currency:"USD",scale:2,start_date:"2026-01-01" };
const response = value => new Response(JSON.stringify(value),{headers:{"content-type":"application/json"}});

test("pending metadata and pending artwork never display incorrect initials or an unknown glyph", async () => {
  await withDOM(async ({render,window}) => {
    const style=document.createElement("style");style.textContent=await readFile(new URL("../src/components/ui/account-avatar.css",import.meta.url),"utf8");document.head.append(style);
    const {ContactAvatar}=await loadComponent("src/components/ui/contact-avatar.tsx");
    const view=(pending,institution)=>React.createElement(ContactAvatar,{book:book.id,identityKey:"unknown",name:"",base:"https://assets.example",brand:{state:"proposed",name:"Example Search"},pending,institution});
    await render(view(true,undefined));
    const avatar=document.querySelector("[data-counterparty-avatar]");
    const disk=()=>avatar.querySelector(".account-avatar-disk");
    assert.equal(getComputedStyle(disk()).visibility,"hidden");
    await render(view(false,search));
    assert.equal(getComputedStyle(disk()).visibility,"hidden");
    const image=avatar.querySelector("img");
    const pixels=new Uint8ClampedArray(512*512*4);for(let y=240;y<272;y++)for(let x=240;x<272;x++)pixels[(y*512+x)*4+3]=255;
    Object.defineProperties(image,{naturalWidth:{value:512},naturalHeight:{value:512}});
    window.HTMLCanvasElement.prototype.getContext=()=>({drawImage(){},getImageData(){return{data:pixels};}});
    await React.act(async()=>image.dispatchEvent(new window.Event("load")));
    assert.ok(avatar.querySelector(".institution-logo[data-loaded]"));
    assert.equal(avatar.style.getPropertyValue("--account-avatar-size"),"24px");
    // Returning to the view starts with prepared artwork rather than replaying fallback states.
    await render(null);await render(view(false,search));
    assert.ok(document.querySelector(".institution-logo[data-loaded]"));
    assert.equal(document.querySelector("[data-avatar-pending]"),null);
    const fresh=await loadComponent("src/lib/circular-mark-fit.ts");
    assert.ok(fresh.cachedMarkFit(`https://assets.example/sha256/${search.asset.sha256}.svg`));
  });
});

test("a true artwork failure reveals the final initials without an intermediate unknown", async () => {
  await withDOM(async ({render,window})=>{
    const style=document.createElement("style");style.textContent=await readFile(new URL("../src/components/ui/account-avatar.css",import.meta.url),"utf8");document.head.append(style);
    const {ContactAvatar}=await loadComponent("src/components/ui/contact-avatar.tsx");
    await render(React.createElement(ContactAvatar,{book:book.id,identityKey:"unknown",name:"",base:"https://failed.example",institution:search,brand:{state:"identified",brand_id:"example-search"}}));
    const avatar=document.querySelector("[data-counterparty-avatar]");
    assert.equal(getComputedStyle(avatar.querySelector(".account-avatar-disk")).visibility,"hidden");
    await React.act(async()=>avatar.querySelector("img").dispatchEvent(new window.Event("error")));
    assert.equal(avatar.textContent,"ES");assert.equal(avatar.hasAttribute("data-avatar-unknown"),false);
    assert.equal(getComputedStyle(avatar.querySelector(".account-avatar-disk")).visibility,"visible");
  });
});

test("public catalogs survive a document reload, stay purpose-scoped and retain valid data on revalidation failure", async()=>{
  await withDOM(async({window})=>{
    const saved=globalThis.fetch;let requests=0;
    globalThis.fetch=async()=>{requests++;return response({version:1,institutions:[{...search,private_user_id:"private-sentinel"}],private_transaction_id:"private-sentinel"});};
    try{
      const first=await loadComponent("src/lib/shared-institutions.ts");
      await first.readInstitutionCatalog("https://assets.example",false,"brand-logos.v1.json");
      const stored=window.sessionStorage.getItem("vult:public-avatar-catalog:v1:brand-logos.v1.json");
      assert.ok(stored);assert.ok(!stored.includes("private-sentinel"));
      const nextDocument=await loadComponent("src/lib/shared-institutions.ts");
      assert.equal(nextDocument.cachedInstitutionCatalog("https://assets.example","brand-logos.v1.json").institutions[0].id,"example-search");
      assert.equal(nextDocument.cachedInstitutionCatalog("https://assets.example","catalog.v1.json"),undefined);
      await nextDocument.readInstitutionCatalog("https://assets.example",false,"brand-logos.v1.json");assert.equal(requests,1);
      globalThis.fetch=async()=>{throw Error("offline");};
      await assert.rejects(nextDocument.readInstitutionCatalog("https://assets.example",true,"brand-logos.v1.json"));
      assert.equal(nextDocument.cachedInstitutionCatalog("https://assets.example","brand-logos.v1.json").institutions[0].id,"example-search");
    }finally{globalThis.fetch=saved;}
  });
});

test("Activity starts the public catalog read in parallel without blocking financial rows",async()=>{
  await withDOM(async()=>{
    const meta=document.createElement("meta");meta.name="vult-shared-asset-origin";meta.content="https://assets.parallel.example";document.head.append(meta);
    const saved=globalThis.fetch;let finishCatalog;const requests=[];
    globalThis.fetch=async input=>{const url=String(input);requests.push(url);if(url.startsWith("https://assets.parallel.example"))return new Promise(resolve=>{finishCatalog=resolve;});return response({book,transactions:[{brand:{state:"proposed",name:"Example Search"}}]});};
    try{
      const {readBookState}=await loadComponent("src/lib/book-state.ts");
      const state=await readBookState(book.id,{},"UTC",{view:"transactions"});
      assert.equal(state.transactions[0].brand.name,"Example Search");
      assert.equal(requests.filter(url=>url.includes("entry-brands")).length,0);
      assert.ok(requests[0].startsWith("https://assets.parallel.example"));
      finishCatalog(response({version:1,institutions:[search]}));
      await new Promise(resolve=>setTimeout(resolve,0));
    }finally{globalThis.fetch=saved;}
  });
});

test("disabled public browser storage does not prevent catalog loading",async()=>{
  await withDOM(async({window})=>{
    Object.defineProperty(window,"sessionStorage",{get(){throw Error("storage denied");}});
    const saved=globalThis.fetch;globalThis.fetch=async()=>response({version:1,institutions:[search]});
    try{const cache=await loadComponent("src/lib/shared-institutions.ts");assert.equal((await cache.readInstitutionCatalog("https://storage-denied.example",false,"brand-logos.v1.json")).institutions[0].id,"example-search");}finally{globalThis.fetch=saved;}
  });
});

test("an evicted cached image cannot hold financial rows behind a network request",{timeout:2000},async()=>{
  await withDOM(async({window})=>{
    const base="https://decode-budget.example";
    const meta=document.createElement("meta");meta.name="vult-shared-asset-origin";meta.content=base;document.head.append(meta);
    window.sessionStorage.setItem("vult:public-avatar-catalog:v1:brand-logos.v1.json",JSON.stringify({base,expires:Date.now()+600000,catalog:{version:1,institutions:[search]}}));
    window.sessionStorage.setItem("vult:public-avatar-fit:512:v2",JSON.stringify([[`${base}/sha256/${search.asset.sha256}.svg`,{scale:2,radiusRatio:.5,offsetX:0,offsetY:0}]]));
    const saved=globalThis.fetch;globalThis.fetch=async()=>response({book,transactions:[{brand:{state:"identified",brand_id:"example-search"}}]});
    try{
      const {readBookState}=await loadComponent("src/lib/book-state.ts");const started=Date.now();
      const state=await readBookState(book.id,{},"UTC",{view:"transactions"});
      assert.equal(state.transactions[0].brand.brand_id,"example-search");assert.ok(Date.now()-started<1000);
    }finally{globalThis.fetch=saved;}
  });
});

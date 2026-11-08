import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import { React, withDOM, loadComponent } from "./helpers/dom.mjs";
import { loadApplication } from "./helpers/application.mjs";

const goldens=JSON.parse(await readFile(new URL("./fixtures/report-exports.json",import.meta.url),"utf8"));
const { loadMonthDisplay,monthRequest,reportMonths }=await loadComponent("src/lib/financial-report-workspace.ts");
const { readBookState }=await loadComponent("src/lib/book-state.ts");
const { StatementsWorkspace }=await loadComponent("src/components/statements/statements-workspace.tsx");
const { default:BookScreens }=await loadComponent("src/components/workspaces/book-screens.tsx");
const response=value=>new Response(JSON.stringify(value),{headers:{"Content-Type":"application/json"}});

test("monthly reads use one captured response and reject mixed snapshots, missing months and scope changes",async()=>{
 await withDOM(async()=>{
  const base=structuredClone(goldens.find(x=>x.name==="profit_loss").result),book=base.book,range=base.scope.period;
  const months=reportMonths(range).map(period=>{
   const request=monthRequest(base,period),source=structuredClone(base);
   source.request=request;source.scope.comparison={from:request.compare_from,to:request.compare_to};
   source.columns.find(c=>c.id==="prior").period=source.scope.comparison;
   return source;
  });
  const original=globalThis.fetch,calls=[];let payload={schema_version:1,base,months};
  globalThis.fetch=async(url,options)=>{calls.push({url:new URL(url,window.location.origin),options});return response(payload)};
  try{
   const controller=new AbortController();const display=await loadMonthDisplay(book,base.request,range,controller.signal);
   assert.equal(calls.length,1);assert.equal(calls[0].url.pathname,"/api/v1/financial-reports/months");assert.equal(calls[0].options.signal,controller.signal);assert.equal(display.sources.length,13);
   assert.equal(display.base.reconciliation.result,base.reconciliation.result);
   for(const corrupt of [p=>p.months.pop(),p=>p.months[0].scope.knowledge_cutoff="different",p=>p.months[0].scope.population_digest="c".repeat(64),p=>p.months[1].request.compare_to=p.months[0].request.compare_to,p=>p.months[0].book.id="other"]){payload=structuredClone({schema_version:1,base,months});corrupt(payload);await assert.rejects(loadMonthDisplay(book,base.request,range));}
   let finish;globalThis.fetch=()=>new Promise(resolve=>{finish=resolve});const cancelled=new AbortController(),pending=loadMonthDisplay(book,base.request,range,cancelled.signal);cancelled.abort();finish(response({schema_version:1,base,months}));await assert.rejects(pending,{name:"AbortError"});
  }finally{globalThis.fetch=original}
 });
});

test("workspace bootstraps request no unrelated financial state; ordinary Accounts and Activity remain full scoped reads",async()=>{
 await withDOM(async()=>{
  const original=globalThis.fetch,calls=[];globalThis.fetch=async url=>{const q=new URL(url,window.location.origin);calls.push(q);return response({book:{id:"book-one"},bootstrap:q.searchParams.has("bootstrap")})};
  try{
   for(const view of ["documents","settings","statements","accounts","transactions"]){const state=await readBookState("book-one",{},"UTC",{view});assert.equal(state.bootstrap,["documents","settings","statements"].includes(view));assert.equal(calls.at(-1).searchParams.get("view"),view)}
   assert.equal(calls[2].searchParams.get("view"),"statements","Statements must not request dashboard projections");
  }finally{globalThis.fetch=original}
 });
});

test("Statements waits for its own workspace bootstrap instead of starting and repeating the previous page's read",async()=>{
 await withDOM(async({render,window})=>{
  const base=structuredClone(goldens.find(x=>x.name==="balance_sheet").result),book=base.book;
  window.history.replaceState({},"","/statements?as_of=2024-12-31&compare=previous_year");
  const original=globalThis.fetch,calls=[];globalThis.fetch=async url=>{calls.push(String(url));return response(base)};
  const mount=state=>render(React.createElement(StatementsWorkspace,{book,state,onShowTransactions(){},onInspect(){}}));
  try{
   await mount({book,read_view:"accounts"});assert.equal(calls.length,0);
   await mount({book,read_view:"statements",bootstrap:true});assert.equal(calls.length,1);assert.ok(document.querySelector(".financial-grid"));
  }finally{globalThis.fetch=original}
 });
});

test("an inspector opened from a Statements bootstrap loads full correction context and preserves account names and GL codes",async()=>{
 await withDOM(async({render})=>{
  const {EntryDetail}=await loadApplication(),book={id:"11111111-1111-4111-8111-111111111111",name:"Example",profile:"business",currency:"USD",scale:2};
  const thin={book,bootstrap:true,currencies:[{code:"USD",name:"US Dollar",scale:2}],accounts:[],cash_accounts:[],payables:[]};
  const full={...thin,bootstrap:undefined,accounts:[{code:"5000",gl_code:"6500",name:"Software",kind:"expense"}]};
  const entry={id:"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",book_id:book.id,kind:"expense",payer:"business",category:"5000",description:"Software purchase",date:"2026-10-12",amount_minor:"10000",allocations:[],lines:[{id:"line-one",account_code:"5000",side:"debit",amount_minor:"10000"}],fx:{original_currency:"USD",original_amount_minor:"10000",paid_currency:"USD",paid_amount_minor:"10000",selection:"same-currency"}};
  const original=globalThis.fetch,calls=[];
  globalThis.fetch=async url=>{const q=new URL(url,window.location.origin);calls.push(q);return response(q.pathname.endsWith("/state")?full:q.pathname.endsWith("/documents")?[]:entry)};
  const mount=state=>render(React.createElement(EntryDetail,{book,id:entry.id,currencies:thin.currencies,state,onSaved(){},onReplace(){},onNavigate(){}}));
  try{
   await mount(thin);assert.equal(document.querySelector(".entry-posting-name").title, "6500 · Software");
   assert.equal(calls.filter(q=>q.pathname.endsWith("/state")).length,1);
   await mount({...thin});assert.equal(calls.filter(q=>q.pathname.endsWith("/state")).length,1);
   assert.match(document.querySelector(".entry-postings").textContent,/\$100\.00/);
  }finally{globalThis.fetch=original}
 });
});

test("a first financial visit waits for full context without mounting a legacy view or inventing zero balances",async()=>{
 await withDOM(async({render})=>{
  const original=globalThis.fetch,reads=[];globalThis.fetch=async url=>{reads.push(String(url));throw Error("Metadata alone must not start a financial view")};
  const noop=()=>{},book={id:"first-visit",name:"Example",profile:"business",currency:"USD",scale:2};
  const props={book,onPosted:noop,onOpening:noop,onNavigate:noop,onShowTransactions:noop,onInspect:noop,onAccount:noop,onSettings:noop,onStart:noop};
  const thin={book,bootstrap:true,currencies:[],accounts:[],cash_accounts:[],payables:[]};
  try{
   for(const page of ["accounts","dashboard"]){await render(React.createElement(BookScreens,{...props,key:page,page,state:thin}));assert.equal(reads.length,0);assert.equal(document.querySelector('[data-testid="v0-book-overview"]'),null);assert.doesNotMatch(document.body.textContent,/No.*accounts|\$0\.00|No recorded balances/)}
  }finally{globalThis.fetch=original}
 });
});

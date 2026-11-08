import assert from "node:assert/strict";
import test from "node:test";
import { React,loadComponent,withDOM } from "./helpers/dom.mjs";

test("direct recognition loading avoids the legacy summary and ignores canceled periods",async()=>{
 await withDOM(async({render,window})=>{
  const {useDashboard}=await loadComponent("src/lib/use-dashboard.ts");
  window.history.replaceState(null,"","/dashboard?from=2026-10-01&to=2026-10-31");
  const book={id:"synthetic-book",start_date:"2026-01-01",currency:"USD",scale:2};
  const requests=[];let result,active=true;
  const load=(range,signal)=>new Promise((resolve,reject)=>requests.push({range,signal,resolve,reject}));
  const original=globalThis.fetch;globalThis.fetch=()=>{throw new Error("Unexpected legacy network read")};
  function Harness(){result=useDashboard(book,undefined,undefined,load,active);return React.createElement("output",null,result.data?.profit_minor||"waiting")}
  const snapshot=r=>({book_id:book.id,period:r,version:"1",profit_minor:r.from});
  try{
   await render(React.createElement(Harness));assert.equal(requests.length,1);
   await React.act(async()=>requests[0].resolve(snapshot(requests[0].range)));assert.equal(result.data.period.from,"2026-10-01");
   await React.act(()=>{void result.selectRange({from:"2026-09-01",to:"2026-09-30"})});
   await React.act(()=>{void result.selectRange({from:"2026-08-01",to:"2026-08-31"})});
   assert.equal(requests[1].signal.aborted,true);
   await React.act(async()=>{requests[2].resolve(snapshot(requests[2].range));requests[1].resolve(snapshot(requests[1].range))});
   assert.equal(result.data.period.from,"2026-08-01");
   await React.act(()=>{void result.selectRange({from:"2026-07-01",to:"2026-07-31"})});
   await React.act(async()=>requests[3].reject(new Error("offline")));
   assert.equal(result.data.period.from,"2026-08-01");assert.equal(result.pending,false);
   active=false;await render(React.createElement(Harness));assert.equal(requests[3].signal.aborted,true);
   window.history.replaceState(null,"","/accounts?from=2026-06-01&to=2026-06-30");
   active=true;await render(React.createElement(Harness));assert.equal(requests.length,5);
   assert.deepEqual(requests[4].range,{from:"2026-06-01",to:"2026-06-30"});
   await React.act(async()=>requests[4].resolve(snapshot(requests[4].range)));
   assert.equal(result.data.period.to,"2026-06-30");
  }finally{globalThis.fetch=original}
 });
});

test("compact cash reads verify exact scope, preserve unknowns and propagate cancellation",async()=>{
 const {loadCashSummary}=await loadComponent("src/lib/cash-summary.ts");
 const book={id:"synthetic-book",currency:"USD",scale:2},period={from:"2026-10-01",to:"2026-10-31"};
 const payload={book_id:book.id,currency:"USD",scale:2,period,revision:"a".repeat(64),cash_minor:"31898",cash_accounts:[],money_in_minor:"0",money_out_minor:"0",movement_count:0};
 const original=globalThis.fetch;let request,signal;
 globalThis.fetch=async(url,options)=>{request=new URL(url,"http://localhost");signal=options.signal;return new Response(JSON.stringify(payload))};
 try{
  const control=new AbortController();assert.equal((await loadCashSummary(book,period,control.signal)).cash_minor,"31898");
  assert.equal(request.pathname,"/api/v1/dashboard/cash-summary");assert.equal(request.searchParams.get("book_id"),book.id);assert.equal(signal,control.signal);
  for(const mismatch of [{book_id:"other"},{currency:"EUR"},{scale:0},{period:{...period,to:"2026-10-30"}},{cash_minor:"3.18"}]){
   globalThis.fetch=async()=>new Response(JSON.stringify({...payload,...mismatch}));await assert.rejects(loadCashSummary(book,period));
  }
  globalThis.fetch=async()=>new Response(JSON.stringify({...payload,cash_minor:undefined,position_error:"Bank evidence missing"}));
  assert.equal((await loadCashSummary(book,period)).cash_minor,undefined);
 }finally{globalThis.fetch=original}
});

test("a refreshed visible Accounts period does not restart a different pending period", () =>
 withDOM(async ({ render, window }) => {
  const { useDashboard } = await loadComponent("src/lib/use-dashboard.ts");
  window.history.replaceState(null, "", "/accounts?from=2026-10-01&to=2026-10-31");
  const book = { id: "synthetic-book", name: "Synthetic Book", start_date: "2026-01-01", currency: "USD", scale: 2 };
  const period = { from: "2026-10-01", to: "2026-10-31" };
  const requests = [];
  const load = (range, signal) => new Promise((resolve, reject) => requests.push({ range, signal, resolve, reject }));
  let result;
  function Harness({ version }) {
   result = useDashboard(book, { book_id: book.id, period, version }, undefined, load, true, true);
   return React.createElement("output", null, result.data?.version);
  }
  await render(React.createElement(Harness, { version: "first" }));
  await React.act(() => { void result.selectRange({ from: "2026-09-01", to: "2026-09-30" }); });
  await render(React.createElement(Harness, { version: "refreshed" }));
  assert.equal(requests.length, 1);
  assert.equal(requests[0].signal.aborted, false);
  assert.equal(result.data.version, "refreshed");
  await React.act(async () => requests[0].reject(new Error("offline")));
  assert.equal(result.data.version, "refreshed");
  assert.equal(result.pending, false);
 }));

import assert from "node:assert/strict";
import test from "node:test";
import {React,loadComponent,withDOM} from "./helpers/dom.mjs";
import {activityData,activitySeries,activityResponse} from "./helpers/activity.mjs";

const book={id:"book-a",name:"Synthetic",currency:"USD",scale:2,start_date:"2026-01-01",accounting_today:"2026-10-25",accounting_timezone:"UTC"};
const activity=activityData(book,{cash:{weekly:activitySeries(2),total:3,last_date:"2026-10-25"},income:{weekly:activitySeries(1),total:1,last_date:"2026-10-24"}});
const response=data=>new Response(JSON.stringify(data),{headers:{"Content-Type":"application/json"}});
const positions=[
 {code:"income",name:"Alpha income",kind:"revenue",usage:"general",currency:"",scale:2,balance_minor:"-4000",native_minor:"",posting_count:1},
 {code:"cash",name:"Zed cash",kind:"asset",usage:"cash",currency:"USD",scale:2,balance_minor:"5000",native_minor:"5000",posting_count:3},
];

test("bounded activity metadata preserves complete counts, dates and chart windows",async()=>{
 const {parseAccountActivity,activityFrom}=await loadComponent("src/lib/account-activity.ts");
 const result=parseAccountActivity(activity,book,book.accounting_today);
 assert.equal(activityFrom(book.accounting_today,52),"2025-10-27");
 assert.equal(activityFrom(book.accounting_today,13),"2026-07-27");
 assert.equal(result.accounts.cash.weekly.length,52);
 assert.equal(result.accounts.cash.weekly.reduce((a,b)=>a+(b||0),0),2);
 assert.equal(result.accounts.income.weekly.reduce((a,b)=>a+(b||0),0),1);
 assert.equal(result.accounts.cash.total,3,"old imported history remains lifetime activity");
 assert.equal(result.accounts.cash.lastDate,"2026-10-25");
 assert.equal(result.accounts.income.lastDate,"2026-10-24");
});

test("unknown history stays a gap and malformed or foreign aggregates cannot become zero activity",async()=>{
 const {parseAccountActivity}=await loadComponent("src/lib/account-activity.ts");
 const unknown={...activityData(book),empty_weekly:[...Array(49).fill(null),0,0,0]};
 const result=parseAccountActivity(unknown,book,book.accounting_today);
 assert.equal(result.emptyWeekly[0],null);assert.equal(result.emptyWeekly.at(-1),0);
 for(const changed of [{...activity,book_id:"other"},{...activity,from:"2020-01-01"},{...activity,as_of:"2026-10-24"},{...activity,empty_weekly:[0]},{...activity,accounts:{cash:{weekly:activitySeries(1),total:-1,last_date:"2026-10-25"}}},{...activity,accounts:{cash:{weekly:activitySeries(1),total:1,last_date:"2026-02-30"}}}])
  assert.throws(()=>parseAccountActivity(changed,book,book.accounting_today),/another Book|invalid|Invalid/i);
});

test("the real Activity list sorts comparable charts and opens the exact account/window without posting",async()=>{
 await withDOM(async({render,window})=>{
  const {AccountActivityList}=await loadComponent("src/components/workspaces/account-activity-list.tsx");
  const original=globalThis.fetch,calls=[],opened=[];
  globalThis.fetch=async(url,init)=>{calls.push({url,method:init?.method||"GET"});return response(activity)};
  try {
   await render(React.createElement(AccountActivityList,{accounts:positions,book,refreshKey:"initial",onOpen:range=>opened.push(range)}));
   await React.act(async()=>new Promise(resolve=>setTimeout(resolve,20)));
   const rows=[...document.querySelectorAll("tr[data-account-code]")];
   assert.equal(rows[0].dataset.accountCode,"cash");
   assert.deepEqual([...document.querySelectorAll("svg[data-activity-scale]")].map(svg=>svg.dataset.activityScale),["2","2"]);
   assert.equal(document.querySelectorAll(".account-activity-summary").length,0,"per-account counts stay removed");
   assert.match(rows[1].textContent,/\$40\.00/);
   await React.act(async()=>rows[0].querySelector(".account-activity-name").click());
   assert.deepEqual(opened,[{from:"2025-10-27",to:"2026-10-25",account:"cash"}]);
   assert.deepEqual(calls,[{url:"/api/v1/account-activity?as_of=2026-10-25&book_id=book-a",method:"GET"}]);
   const sort=document.querySelector('select[aria-label="Sort accounts"]');
   await React.act(async()=>{sort.value="name";sort.dispatchEvent(new window.Event("change",{bubbles:true}))});
   assert.equal(document.querySelector("tr[data-account-code]").dataset.accountCode,"income");
   assert.equal(calls.length,1,"presentation changes reuse the same complete history");
  } finally {globalThis.fetch=original;}
 });
});

test("a late history read cannot overwrite the newly selected Book",async()=>{
 await withDOM(async({render})=>{
  const {AccountActivityList}=await loadComponent("src/components/workspaces/account-activity-list.tsx");
  const original=globalThis.fetch;
  let finishOld;
  globalThis.fetch=async url=>String(url).endsWith("book-a")?new Promise(resolve=>{finishOld=resolve}):activityResponse({...book,id:"book-b"},{cash:{weekly:activitySeries(1),total:1,last_date:"2026-10-25"}});
  try {
   const view=book=>React.createElement(AccountActivityList,{accounts:positions,book,refreshKey:"same"});
   await render(view(book));
   assert.equal(document.querySelectorAll("svg[data-activity-scale]").length,0,"pending history must not draw invented zero charts");
   await render(view({...book,id:"book-b"}));
   await React.act(async()=>new Promise(resolve=>setTimeout(resolve,20)));
   await React.act(async()=>{finishOld(response(activity));await new Promise(resolve=>setTimeout(resolve,20))});
   assert.equal(document.querySelector('[data-account-code="cash"] svg[data-activity-scale]').dataset.activityScale,"1","the late first-Book response must not replace the current Book series");
  } finally {globalThis.fetch=original;}
 });
});

test("unavailable history keeps balances, draws no invented charts, and retries the same scoped read",async()=>{
 await withDOM(async({render})=>{
  const {AccountActivityList}=await loadComponent("src/components/workspaces/account-activity-list.tsx");
  const original=globalThis.fetch,calls=[];
  globalThis.fetch=async url=>{calls.push(String(url));return calls.length===1?new Response("<html>Unavailable</html>",{headers:{"Content-Type":"text/html"}}):response(activity)};
  try {
   await render(React.createElement(AccountActivityList,{accounts:positions,book,refreshKey:"retry"}));
   await React.act(async()=>new Promise(resolve=>setTimeout(resolve,20)));
   assert.match(document.body.textContent,/load the activity/);
   assert.match(document.body.textContent,/\$50\.00/);
   assert.equal(document.querySelectorAll("svg[data-activity-scale]").length,0);
   const retry=[...document.querySelectorAll("button")].find(button=>button.textContent==="Try again");
   assert.ok(retry);
   await React.act(async()=>retry.click());
   await React.act(async()=>new Promise(resolve=>setTimeout(resolve,20)));
   assert.equal(document.querySelectorAll("svg[data-activity-scale]").length,2);
   assert.equal(calls.length,2);
   assert.equal(calls[0],calls[1]);
  } finally {globalThis.fetch=original;}
 });
});

test("recorded names stay whole (separators never make parents), with explicit bank suffixes and exact drilldown identities",async()=>{
 await withDOM(async({render,window})=>{
  const {AccountActivityList}=await loadComponent("src/components/workspaces/account-activity-list.tsx");
  const original=globalThis.fetch,opened=[];
  const named=[
   {...positions[1],name:"Zzz:Operating •• 1234"},
   {...positions[0],name:"Costs:Service fees",kind:"expense",currency:"EUR",native_minor:"4000",balance_minor:"4000"},
   {...positions[0],code:"fees-b",name:"Other costs:Service fees",kind:"expense",balance_minor:"1000"},
  ];
  globalThis.fetch=async()=>response(activity);
  try {
   await render(React.createElement(AccountActivityList,{accounts:named,book,refreshKey:"identity",onOpen:range=>opened.push(range)}));
   await React.act(async()=>new Promise(resolve=>setTimeout(resolve,20)));
   const first=document.querySelector('[data-account-code="cash"]');
   assert.equal(first.querySelector('.account-activity-label').textContent,"Zzz:Operating");
   assert.equal(first.querySelector('.account-activity-suffix').textContent,"•• 1234");
   assert.equal(first.querySelector('.account-activity-name').title,"Zzz:Operating •• 1234");
   assert.match(first.querySelector('.account-activity-sparkline title').textContent,/Last entry October 25, 2026/);
   const fees=document.querySelector('[data-account-code="income"]');
   const other=document.querySelector('[data-account-code="fees-b"]');
   assert.equal(fees.querySelector('.account-activity-label').textContent,"Costs:Service fees");
   assert.equal(other.querySelector('.account-activity-label').textContent,"Other costs:Service fees");
   assert.match(fees.querySelector('.account-activity-value').textContent,/\$40\.00/);
   assert.match(fees.querySelector('.account-activity-native').textContent,/€40\.00/);
   await React.act(async()=>other.querySelector('.account-activity-name').click());
   assert.deepEqual(opened,[{from:"2025-10-27",to:"2026-10-25",account:"fees-b"}]);
   const sort=document.querySelector('select[aria-label="Sort accounts"]');
   await React.act(async()=>{sort.value="name";sort.dispatchEvent(new window.Event("change",{bubbles:true}))});
   assert.equal(document.querySelector('[data-account-code]').dataset.accountCode,"income","Name sort follows the whole recorded name");
  } finally {globalThis.fetch=original;}
 });
});


test("a recorded account with no entries has valid empty history, while missing dates cannot hide activity", async () => {
 const {parseAccountActivity}=await loadComponent("src/lib/account-activity.ts");
 const empty={...activityData(book),accounts:{new_account:{weekly:activitySeries(),total:0,last_date:""}}};
 assert.equal(parseAccountActivity(empty,book,book.accounting_today).accounts.new_account.total,0);
 for(const corrupt of [{weekly:activitySeries(),total:1,last_date:""},{weekly:activitySeries(1),total:0,last_date:""}])
  assert.throws(()=>parseAccountActivity({...empty,accounts:{new_account:corrupt}},book,book.accounting_today),/invalid/);
});

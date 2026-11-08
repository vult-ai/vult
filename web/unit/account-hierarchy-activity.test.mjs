import assert from "node:assert/strict";
import test from "node:test";
import {readFile} from "node:fs/promises";
import {React,loadComponent,withDOM} from "./helpers/dom.mjs";

const header="book_id,transaction_id,account_code,economic_date,recorded_at,note\r\n";
const book={id:"book-a",name:"Synthetic",currency:"USD",scale:2,start_date:"2026-01-01",accounting_today:"2026-10-25",accounting_timezone:"UTC"};
const csv=header+[
 'book-a,one,cash,2026-10-24,2026-10-25,"First line, quoted"',
 'book-a,one,cash,2026-10-24,2026-10-25,"Another line for the same account\nwith an escaped ""quote"""',
 'book-a,one,income,2026-10-24,2026-10-25,Counterpart',
 'book-a,two,cash,2026-10-25,2026-10-25,Distinct transaction',
 'book-a,old,cash,2020-01-01,2026-10-25,Old import',
 'book-a,future,income,2026-10-30,2026-10-25,Future date',
].join("\r\n")+"\r\n";
const response=text=>new Response(text,{headers:{"Content-Type":"text/csv; charset=utf-8"}});
const positions=[
 {code:"income",name:"Alpha income",kind:"revenue",usage:"general",currency:"",scale:2,balance_minor:"-4000",native_minor:"",posting_count:1},
 {code:"cash",name:"Zed cash",kind:"asset",usage:"cash",currency:"USD",scale:2,balance_minor:"5000",native_minor:"5000",posting_count:3},
];

test("economic-date weekly activity deduplicates split lines per account and excludes imports and future dates from the current window",async()=>{
 const {parseJournalAccountActivity,activityFrom}=await loadComponent("src/lib/account-hierarchy-activity.ts");
 const result=parseJournalAccountActivity(csv,book,book.accounting_today);
 assert.equal(activityFrom(book.accounting_today,52),"2025-10-27");
 assert.equal(activityFrom(book.accounting_today,13),"2026-07-27");
 assert.equal(result.accounts.cash.weekly.length,52);
 assert.equal(result.accounts.cash.weekly.reduce((a,b)=>a+(b||0),0),2);
 assert.equal(result.accounts.income.weekly.reduce((a,b)=>a+(b||0),0),1);
 assert.equal(result.accounts.cash.total,3,"old imported history remains lifetime activity");
 assert.equal(result.accounts.cash.lastDate,"2026-10-25");
 assert.equal(result.accounts.income.lastDate,"2026-10-24");
});

test("unknown pre-Book history stays a gap and malformed or foreign history cannot become zero activity",async()=>{
 const {parseJournalAccountActivity}=await loadComponent("src/lib/account-hierarchy-activity.ts");
 const result=parseJournalAccountActivity(header,{...book,start_date:"2026-10-01"},book.accounting_today);
 assert.equal(result.emptyWeekly[0],null);
 assert.equal(result.emptyWeekly.at(-1),0);
 assert.throws(()=>parseJournalAccountActivity(csv.replace('"First line, quoted"','"unclosed'),book,book.accounting_today),/invalid|truncated/i);
 assert.throws(()=>parseJournalAccountActivity(csv.replace("book-a,one,cash","other-book,one,cash"),book,book.accounting_today),/another Book/);
 assert.throws(()=>parseJournalAccountActivity(csv.replace("book-a,one,income,2026-10-24","book-a,one,income,2026-10-23"),book,book.accounting_today),/conflicting/);
 assert.throws(()=>parseJournalAccountActivity(csv.replace("2026-10-24","2026-02-30"),book,book.accounting_today),/Invalid activity date/);
 assert.throws(()=>parseJournalAccountActivity("<html>Not a CSV</html>",book,book.accounting_today),/invalid/i);
});


test("a read of every account narrows to the accounts shown, and a group stands in only when its extra accounts never recorded an entry",async()=>{
 const {narrowHierarchyActivity}=await loadComponent("src/lib/account-hierarchy-activity.ts");
 const weeks=(n)=>Array.from({length:52},(_,i)=>i===51?n:0);
 const read={bookId:"book-a",asOf:"2026-10-25",from:"2025-10-27",emptyWeekly:weeks(0),
  accounts:{cash:{weekly:weeks(2),total:2,lastDate:"2026-10-25"},savings:{weekly:weeks(1),total:1,lastDate:"2026-10-24"},unused:{weekly:weeks(0),total:0,lastDate:""}},
  groups:new Map([["cash",{weekly:weeks(2),count:2,lastDate:"2026-10-25"}],["savings",{weekly:weeks(1),count:1,lastDate:"2026-10-24"}],["unused",{weekly:weeks(0),count:0,lastDate:""}],
   [["cash","savings","unused"].sort().join("\0"),{weekly:weeks(3),count:3,lastDate:"2026-10-25"}]]),
  nativeBalances:{cash:"1",savings:"2",unused:"0"},nativeCurrencies:{cash:"USD",savings:"USD",unused:"USD"}};
 // The default view hides the account that never recorded anything: the class group stands in.
 const active=narrowHierarchyActivity(read,["cash","savings"],[["cash","savings"]]);
 assert.deepEqual(Object.keys(active.accounts).sort(),["cash","savings"]);
 assert.equal(active.groups.get(["cash","savings"].sort().join("\0")).count,3);
 assert.equal(active.scope,["cash","savings"].sort().join("\0"));
 assert.deepEqual(Object.keys(active.nativeBalances).sort(),["cash","savings"]);
 // A search that leaves out an account with entries changes the group: the read must name its accounts.
 assert.equal(narrowHierarchyActivity(read,["cash","unused"],[["cash","unused"]]),null);
 // Every account shown, every group the server already read.
 assert.ok(narrowHierarchyActivity(read,["cash","savings","unused"],[["cash","savings","unused"]]));
});

test("large account selections use a cancellable CSRF-protected read body", async () => {
 const { fetchAccountSelection } = await loadComponent("src/lib/account-hierarchy-activity.ts");
 const ids = Array.from({ length: 212 }, (_, i) => `00000000-0000-4000-8000-${String(i).padStart(12, "0")}`);
 const original = globalThis.fetch;
 const calls = [];
 globalThis.fetch = async (url, init) => {
  calls.push({ url: String(url), init });
  return Response.json(String(url) === "/auth/session" ? { csrf: "example-token" } : {});
 };
 try {
  const signal = new AbortController().signal;
  await fetchAccountSelection("account-detail", "book", new URLSearchParams({ view: "group" }), signal, ids);
  assert.equal(calls[0].url, "/auth/session");
  assert.equal(calls[0].init.signal, signal);
  const detail = calls[1];
  assert.equal(detail.init.method, "POST");
  assert.equal(detail.init.headers["X-CSRF-Token"], "example-token");
  assert.equal(detail.init.signal, signal);
  assert.deepEqual(JSON.parse(detail.init.body), { codes: ids });
  assert.ok(!detail.url.includes("codes"));
  await fetchAccountSelection("account-hierarchy-activity", "book", new URLSearchParams(), signal, ids.slice(0, 2));
  assert.equal(calls.length, 3, "the short selection does not need a separate session read");
  assert.ok(new URL(calls[2].url, "http://localhost").searchParams.has("codes"));
 } finally { globalThis.fetch = original; }
});

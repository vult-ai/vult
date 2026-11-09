import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowRight, Check, Copy01, Loading01, Lock01, UploadCloud02 } from "../../design-system/icons";
import { get, type Book } from "../../lib/api";
import { bookCurrencyOptions, browserBookCurrency, currencyLabel, currencyScale } from "../../lib/book-currency";
import { readBookState } from "../../lib/book-state";
import { browserTimeZone } from "../../lib/date-format";
import { fetchJSON } from "../../lib/errors";
import { readSession, writeHeaders } from "../../lib/session";
import { settingsPost } from "../../lib/settings-requests";
import { Field, message } from "../form";
import { Button } from "../ui/button";
import { AccountingTimezone } from "./accounting-timezone";
import { currencyTerritories } from "./currency-territories";
import { normalizeCurrencySearch } from "./currency-search";
import { OnboardingChoice } from "./onboarding-choice";
import { OnboardingLayout } from "./onboarding-layout";
import { openCreatedBook } from "../../lib/open-created-book";
import { withRecentSignIn } from "../../lib/recent-sign-in";
import "./welcome.css";

type Connection = { id: string; name: string; connection?: boolean; revoked_at?: string };
type Agent = "claude" | "claude-code" | "other";

const POLL_MS = 4000;
const placeholderNames = new Set(["", "book"]);
const welcomeKey = (bookId: string) => `vult.welcome.${bookId}`;

function readWelcome(bookId: string) {
  try {
    return window.localStorage.getItem(welcomeKey(bookId));
  } catch {
    return null;
  }
}
function writeWelcome(bookId: string, value: "started" | "done") {
  try {
    window.localStorage.setItem(welcomeKey(bookId), value);
  } catch {
    /* Without storage the welcome follows the Book's name alone. */
  }
}

/** A Book still carrying its placeholder name, or one whose welcome the owner
 * started here, may open on the welcome. Named Books open as they always have. */
export function welcomeEligible(book: Book) {
  const seen = readWelcome(book.id);
  if (seen === "done") return false;
  return seen === "started" || ((book.metadata_revision ?? 0) === 0 && placeholderNames.has(book.name.trim().toLowerCase()));
}
export function finishWelcome(bookId: string) {
  writeWelcome(bookId, "done");
}

/** Whether a Book has any recorded entries yet. */
export async function bookHasEntries(book: Book, signal?: AbortSignal) {
  const state = await readBookState(book.id, {}, book.accounting_timezone, { view: "transactions", limit: 1, signal });
  return (state.transactions?.length ?? 0) > 0;
}

/** Polls while the page is visible. The latest callback is read from a ref. */
function usePoll(run: () => void, enabled: boolean) {
  const latest = useRef(run);
  latest.current = run;
  useEffect(() => {
    if (!enabled) return;
    latest.current();
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") latest.current();
    }, POLL_MS);
    return () => window.clearInterval(id);
  }, [enabled]);
}

/** A Vult with no Book yet. On this Mac the first run is its own scene
 * (`local`); a server shows the welcome steps. `fallback` covers a session
 * that owns no Vult. */
export function EmptyVult({ fallback, onCreated, local }: { fallback: ReactNode; onCreated: () => void; local: ReactNode }) {
  const [owner, setOwner] = useState<{ tenant: string; local: boolean } | null | undefined>(undefined);
  useEffect(() => {
    const controller = new AbortController();
    readSession(controller.signal).then(
      (s) => setOwner(s.tenants?.length === 1 ? { tenant: s.tenants[0], local: s.mode === "local" } : null),
      () => { if (!controller.signal.aborted) setOwner(null); },
    );
    return () => controller.abort();
  }, []);
  if (owner === undefined) return null;
  if (owner?.local) return <>{local}</>;
  return (
    <OnboardingLayout>
      {owner === null ? fallback : <Welcome book={null} tenant={owner.tenant} onBookSaved={onCreated} onOpenBook={() => undefined} onUpload={() => undefined} />}
    </OnboardingLayout>
  );
}

type StepState = "done" | "active" | "upcoming";

/** The first thing a new owner sees: name the Book, connect an agent, send
 * statements. Each step confirms itself as soon as Vult sees it happen. */
export function Welcome({
  book: initialBook,
  tenant = "",
  onBookSaved,
  onOpenBook,
  onUpload,
}: {
  book: Book | null;
  tenant?: string;
  onBookSaved: (book: Book) => void;
  onOpenBook: () => void;
  onUpload: () => void;
}) {
  const heading = useId();
  // The saved Book shows at once; the catalog refresh catches up behind it.
  const [book, setBook] = useState(initialBook);
  useEffect(() => { if (initialBook) writeWelcome(initialBook.id, "started"); }, [initialBook?.id]);
  const named = !!book && (book.metadata_revision ?? 0) > 0 && !placeholderNames.has(book.name.trim().toLowerCase());
  const [editing, setEditing] = useState(!named);
  const [agent, setAgent] = useState<Connection | null>(null);
  const [address, setAddress] = useState("");
  const [recorded, setRecorded] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    readSession(controller.signal)
      .then((s) => setAddress(s.agent_address ?? ""))
      .catch(() => { /* The address line explains itself when missing. */ });
    return () => controller.abort();
  }, []);

  usePoll(() => {
    get<{ tokens: Connection[] }>("access/tokens")
      .then((v) => setAgent(v.tokens.find((t) => t.connection !== false && !t.revoked_at) ?? null))
      .catch(() => { /* Checked again on the next pass. */ });
  }, !agent);

  usePoll(() => {
    if (!book) return;
    bookHasEntries(book)
      .then(setRecorded)
      .catch(() => { /* Checked again on the next pass. */ });
  }, !!book && !recorded);

  const setupDone = named && !editing;
  const current = !setupDone ? 1 : !agent ? 2 : !recorded ? 3 : 4;
  const open = () => {
    if (!book) return;
    finishWelcome(book.id);
    onOpenBook();
  };
  const state = (n: number, complete: boolean): StepState =>
    current === n ? "active" : complete ? "done" : "upcoming";

  const steps = [
    {
      n: 1,
      title: "Create your Book",
      summary: setupDone && book ? `${book.name} · ${book.currency}` : "",
      lead: book
        ? "This is how the Book appears to you and to your agent."
        : "A Book keeps the accounts of one person or one business, in one currency.",
      state: state(1, setupDone),
      onEdit: setupDone && !recorded ? () => setEditing(true) : undefined,
    },
    {
      n: 2,
      title: "Connect your agent",
      summary: agent ? `${agent.name} connected` : "",
      lead: "Your agent records entries into this Book. You approve the connection once, in Vult.",
      state: state(2, !!agent),
    },
    {
      n: 3,
      title: "Send your statements",
      summary: recorded ? "Entries are arriving" : "",
      lead: "Your agent reads them and records the entries. Anything it isn’t sure about waits for you.",
      state: state(3, recorded),
    },
  ];
  const finished = current === 4;

  return (
    <section className="welcome" aria-labelledby={heading}>
      <h1 id={heading}>{finished ? "Your Book is live" : "Set up your Book"}</h1>
      <section className="welcome-card" aria-label="Book setup">
        <header className="welcome-card-header">
          <span className="welcome-card-address">{window.location.host}</span>
          <span className="welcome-card-state">
            <span data-ready={finished || undefined} />
            {finished ? "Ready" : `Step ${current} of 3`}
          </span>
        </header>
        <ol className="welcome-steps" aria-live="polite">
          {steps.map((step) => (
            <li key={step.n} data-state={step.state} aria-current={step.state === "active" ? "step" : undefined}>
              <div className="welcome-step-row">
                <span className="welcome-marker" aria-hidden="true">
                  {step.state === "done" ? (
                    <Check />
                  ) : step.state === "active" && step.n > 1 ? (
                    <Loading01 className="welcome-spinner" />
                  ) : (
                    <span className="welcome-dot" />
                  )}
                </span>
                <span className="welcome-step-label">
                  {step.title}
                  {step.state === "done" && <span className="welcome-visually-hidden"> (done)</span>}
                  {step.summary && <span className="welcome-step-summary">{step.summary}</span>}
                </span>
                {step.onEdit && step.state === "done" && (
                  <Button color="tertiary" size="sm" onClick={step.onEdit}>
                    Edit
                  </Button>
                )}
              </div>
              {step.state === "active" && (
                <div className="welcome-step-body">
                  <p className="welcome-lead">{step.lead}</p>
                  {step.n === 1 && !book && (
                    <CreateBook
                      tenant={tenant}
                      onCreated={(created) => {
                        writeWelcome(created.id, "started");
                        onBookSaved(created);
                      }}
                    />
                  )}
                  {step.n === 1 && book && (
                    <BookSetup
                      book={book}
                      onSaved={(saved) => {
                        setBook(saved);
                        onBookSaved(saved);
                        setEditing(false);
                      }}
                    />
                  )}
                  {step.n === 2 && <AgentSetup address={address} />}
                  {step.n === 3 && book && <StatementHandover book={book} onUpload={onUpload} />}
                </div>
              )}
            </li>
          ))}
        </ol>
        <footer className="welcome-card-footer">
          <Lock01 aria-hidden="true" />
          Your Books are private to your Vult.
        </footer>
      </section>
      {!book ? null : finished ? (
        <Button size="lg" className="welcome-primary" onClick={open}>
          Open {book.name} <ArrowRight aria-hidden="true" />
        </Button>
      ) : (
        <Button color="tertiary" size="sm" className="welcome-skip" onClick={open}>
          Skip for now
        </Button>
      )}
    </section>
  );
}

export const currencyItems = bookCurrencyOptions().map((option) => ({
  id: option.value,
  label: option.label,
  search: normalizeCurrencySearch(`${option.label} ${(currencyTerritories[option.value] ?? []).join(" ")}`),
}));

export function PurposeChoice({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const purposeName = useId();
  return (
    <fieldset className="welcome-purpose">
      <legend>What is it for?</legend>
      {[
        { id: "business", label: "Business", detail: "A company’s income, expenses and what it owes" },
        { id: "personal", label: "Personal", detail: "Your own accounts, cards and spending" },
      ].map((option) => (
        <label key={option.id} className="welcome-purpose-choice" data-selected={value === option.id || undefined}>
          <input
            type="radio"
            name={purposeName}
            value={option.id}
            checked={value === option.id}
            onChange={() => onChange(option.id)}
          />
          <span>
            <strong>{option.label}</strong>
            <small>{option.detail}</small>
          </span>
        </label>
      ))}
    </fieldset>
  );
}

/** Creates the Vult's first Book with its currency, then records what it is
 * for and its timezone. */
/** Names a new Book and sets its purpose, currency and timezone. Works on this
 * computer and on a server; the currency is fixed once the Book exists. */
export function CreateBook({ onCreated }: { tenant?: string; onCreated: (book: Book) => void }) {
  const [name, setName] = useState("");
  const [profile, setProfile] = useState("");
  const [currency, setCurrency] = useState(() => browserBookCurrency());
  const [zone, setZone] = useState(browserTimeZone());
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const createKey = useRef(crypto.randomUUID());
  const settingsKey = useRef(crypto.randomUUID());
  const ready = name.trim() !== "" && profile !== "" && currency !== "" && zone !== "";

  async function create() {
    if (!ready) {
      setError(!profile ? "Choose Personal or Business." : !name.trim() ? "Give your Book a name." : !currency ? "Choose a currency." : "Choose a timezone.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const created = await withRecentSignIn(async () =>
        fetchJSON<Book>("/api/v1/books", {
          method: "POST",
          headers: await writeHeaders(),
          body: JSON.stringify({ request_key: createKey.current, name: name.trim(), currency, scale: currencyScale(currency) }),
        }),
      );
      let book = created;
      try {
        book = await settingsPost<Book>("settings", {
          book_id: created.id,
          idempotency_key: settingsKey.current,
          revision: 0,
          name: name.trim(),
          founder: "",
          profile,
          accounting_timezone: zone,
        });
      } catch {
        /* The Book exists; its details can be finished in the next view. */
      }
      onCreated(book);
    } catch (e) {
      setError(message(e));
      setBusy(false);
    }
  }

  return (
    <form
      className="welcome-form"
      onSubmit={(e) => {
        e.preventDefault();
        void create();
      }}
    >
      <PurposeChoice value={profile} onChange={setProfile} />
      <Field label="Book name">
        <input
          name="name"
          maxLength={120}
          autoComplete="organization"
          placeholder={profile === "personal" ? "Your name" : "Your company’s name"}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
      <OnboardingChoice
        label="Currency"
        name="currency"
        value={currency}
        items={currencyItems}
        onChange={setCurrency}
        placeholder="Search currency or country"
      />
      <AccountingTimezone value={zone} onChange={setZone} />
      <p className="welcome-fact">A Book’s currency can’t change after it’s created.</p>
      {error && (
        <p className="welcome-error" role="alert">
          {error}
        </p>
      )}
      <div className="welcome-actions">
        <Button type="submit" busy={busy}>
          Create Book
        </Button>
      </div>
    </form>
  );
}

function BookSetup({ book, onSaved }: { book: Book; onSaved: (book: Book) => void }) {
  const [otherCurrency, setOtherCurrency] = useState(false);
  if (otherCurrency)
    return (
      <>
        <CreateBook onCreated={(created) => void openCreatedBook(created)} />
        <div className="welcome-actions">
          <Button type="button" color="tertiary" onClick={() => setOtherCurrency(false)}>
            Keep {currencyLabel(book.currency)}
          </Button>
        </div>
      </>
    );
  return <BookSetupForm book={book} onSaved={onSaved} onOtherCurrency={() => setOtherCurrency(true)} />;
}

function BookSetupForm({ book, onSaved, onOtherCurrency }: { book: Book; onSaved: (book: Book) => void; onOtherCurrency: () => void }) {
  const [name, setName] = useState(placeholderNames.has(book.name.trim().toLowerCase()) ? "" : book.name);
  const [profile, setProfile] = useState(book.profile === "personal" ? "personal" : book.profile === "business" ? "business" : "");
  const [zone, setZone] = useState(book.accounting_timezone || browserTimeZone());
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());
  const ready = name.trim() !== "" && profile !== "" && zone !== "";

  async function save() {
    if (!ready) {
      setError(!name.trim() ? "Give your Book a name." : !profile ? "Choose Personal or Business." : "Choose a timezone.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      const saved = await settingsPost<Book>("settings", {
        book_id: book.id,
        idempotency_key: key.current,
        revision: book.metadata_revision ?? 0,
        name: name.trim(),
        founder: book.founder ?? "",
        profile,
        accounting_timezone: zone,
      });
      key.current = crypto.randomUUID();
      onSaved(saved);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      className="welcome-form"
      onSubmit={(e) => {
        e.preventDefault();
        void save();
      }}
    >
      <PurposeChoice value={profile} onChange={setProfile} />
      <Field label="Book name">
        <input
          name="name"
          maxLength={120}
          autoComplete="organization"
          placeholder={profile === "personal" ? "Your name" : "Your company’s name"}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </Field>
      <AccountingTimezone value={zone} onChange={setZone} />
      <p className="welcome-fact">
        Amounts are kept in {currencyLabel(book.currency)}. A Book’s currency can’t change.{" "}
        <button type="button" className="welcome-link" onClick={onOtherCurrency}>
          Use another currency
        </button>
      </p>
      {error && (
        <p className="welcome-error" role="alert">
          {error}
        </p>
      )}
      <div className="welcome-actions">
        <Button type="submit" busy={busy}>
          Continue
        </Button>
      </div>
    </form>
  );
}

const agents: { id: Agent; label: string }[] = [
  { id: "claude", label: "Claude" },
  { id: "claude-code", label: "Claude Code" },
  { id: "other", label: "Other agents" },
];

function AgentSetup({ address }: { address: string }) {
  const [agent, setAgent] = useState<Agent>("claude");
  const tabs = useId();
  const command = `claude mcp add --transport http vult ${address}`;
  return (
    <div className="welcome-agent">
      <div className="welcome-agent-tabs" role="radiogroup" aria-label="Your agent">
        {agents.map((a) => (
          <label key={a.id} data-selected={agent === a.id || undefined}>
            <input type="radio" name={tabs} checked={agent === a.id} onChange={() => setAgent(a.id)} />
            {a.label}
          </label>
        ))}
      </div>
      {agent === "claude" && (
        <ol className="welcome-howto">
          <li>
            In Claude, open <strong>Settings</strong>, then <strong>Connectors</strong>.
          </li>
          <li>
            Choose <strong>Add custom connector</strong>, name it Vult and paste:
            <CopyLine text={address} label="Copy address" />
          </li>
          <li>Choose <strong>Connect</strong> and approve Vult in the window that opens.</li>
        </ol>
      )}
      {agent === "claude-code" && (
        <ol className="welcome-howto">
          <li>
            Run this in your terminal:
            <CopyLine text={command} label="Copy command" />
          </li>
          <li>
            In Claude Code, type <code>/mcp</code>, choose Vult and approve it.
          </li>
        </ol>
      )}
      {agent === "other" && (
        <ol className="welcome-howto">
          <li>
            Add an MCP server with this address:
            <CopyLine text={address} label="Copy address" />
          </li>
          <li>Your agent opens a Vult sign-in. Approve it there.</li>
        </ol>
      )}
    </div>
  );
}

function StatementHandover({ book, onUpload }: { book: Book; onUpload: () => void }) {
  const prompt = useMemo(
    () =>
      `Use Vult to record my statements in my Book “${book.name}”. Start with the statements I attach. Ask me before you record anything you’re unsure about.`,
    [book.name],
  );
  return (
    <div className="welcome-handover">
      <ol className="welcome-howto">
        <li>Attach your latest bank and card statements in your agent’s chat.</li>
        <li>
          Send it this:
          <CopyLine text={prompt} label="Copy message" wrap />
        </li>
      </ol>
      <div className="welcome-alternative">
        <span>Prefer to upload them here?</span>
        <Button color="secondary" size="sm" onClick={onUpload}>
          <UploadCloud02 aria-hidden="true" /> Upload statements
        </Button>
      </div>
    </div>
  );
}

function CopyLine({ text, label, wrap = false }: { text: string; label: string; wrap?: boolean }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const id = window.setTimeout(() => setCopied(false), 2000);
    return () => window.clearTimeout(id);
  }, [copied]);
  if (!text) return <p className="welcome-fact">Your agent address isn’t available. Reload this page to try again.</p>;
  return (
    <div className="welcome-copy" data-wrap={wrap || undefined}>
      <code>{text}</code>
      <Button
        color="tertiary"
        size="sm"
        aria-label={copied ? "Copied" : label}
        title={copied ? "Copied" : label}
        onClick={() => {
          void navigator.clipboard?.writeText(text).then(() => setCopied(true), () => undefined);
        }}
      >
        {copied ? <Check aria-hidden="true" /> : <Copy01 aria-hidden="true" />}
      </Button>
    </div>
  );
}

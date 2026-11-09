import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, Copy01, UploadCloud02 } from "../../design-system/icons";
import { money } from "../../lib/api";
import { bookCurrencyOptions, browserBookCurrency, currencyScale } from "../../lib/book-currency";
import { browserTimeZone } from "../../lib/date-format";
import { Field } from "../form";
import { Button } from "../ui/button";
import { VultBrand } from "../ui/vult-brand";
import { AccountingTimezone } from "./accounting-timezone";
import { OnboardingChoice } from "./onboarding-choice";
import { OnboardingParticles } from "./onboarding-particles";
import { AgentStatus, type AgentStatusStep } from "./agent-status";
import { ThemeSwitcher } from "./theme-switcher";
import { PurposeChoice, currencyItems } from "./welcome";
import "./onboarding.css";
import "./entrance.css";

type Stage = "book" | "inside";

export type EntranceDraft = { name: string; profile?: string; currency: string; timezone?: string };
/** What Vult sees the agent do, as it happens. Vult never reads the statement
 * itself; it sees the agent open it and every change the agent makes. */
export type AgentEvent =
  | { kind: "opened"; document: string }
  | { kind: "checked" }
  | { kind: "account"; name: string }
  | { kind: "entry"; entry: EntranceEntry; cited?: string }
  | { kind: "review"; description: string }
  | { kind: "compared" }
  | { kind: "balanced" }
  /** The agent read the file and can't use it as a statement. */
  | { kind: "unusable"; reason: string };
/** An entry as the agent records it. Positive amounts are money in. */
export type EntranceEntry = { date: string; description: string; amountMinor?: string; review?: boolean };
/** The agent's own name for itself, such as Claude Code or Codex. */
export type EntranceAgent = string;

/** How agents reach the local Vult: through its installed command. */
export type EntranceConnection = { kind: "local"; program: string } | { kind: "server"; address: string };

export type Journey = {
  connection: EntranceConnection;
  /** The agent that set this Vult up and is already waiting to be let in. */
  agent?: EntranceAgent;
  /** Resolves with the agent once one asks to be let in. */
  waitForAgent: () => Promise<EntranceAgent>;
  approveAgent: () => Promise<void>;
  /** Notes that the owner chose to start without a statement. */
  skipStatement?: () => void;
  /** Uploads the first statement into the Book. It stays in Vult. */
  addStatement: (file: File) => Promise<void>;
  /** Delivers what the agent does in the Book, until it finishes. */
  watchAgent: (onEvent: (event: AgentEvent) => void) => Promise<void>;
  /** Where an earlier visit left off: the statement uploaded, and whether an
   * agent is already let in. Null when nothing has happened yet. */
  resume?: () => Promise<{ file: string; skipped?: boolean; agent?: EntranceAgent; allowed: boolean } | null>;
};

/** Shape the first Book, then step inside it and bring in the agent and the
 * first statement. Sign-in and recovery codes come first on Vult's sign-in pages. */
export function Entrance({
  start = "book",
  draft,
  book,
  createBook,
  journey,
  onOpen,
}: {
  /** Start with a new Book, or continue inside one created on an earlier visit. */
  start?: Stage;
  draft?: EntranceDraft | null;
  /** A Book created on an earlier visit, to continue inside it. */
  book?: { name: string; profile?: string; currency: string };
  createBook: (book: { name: string; profile: string; currency: string; timezone: string }) => Promise<void>;
  journey: Journey;
  /** Leaves the scene for the Book, or for the entries waiting for review. */
  onOpen?: (page: "dashboard" | "approvals") => void;
}) {
  const [stage, setStage] = useState<Stage>(start);
  return (
    <div className="onboarding entrance" data-stage={stage}>
      <OnboardingParticles className="onboarding-particles" aria-hidden="true" color="#666666" ease={20} quantity={120} />
      <header className="entrance-header">
        <span className="entrance-logo">
          <VultBrand />
        </span>
        <ThemeSwitcher />
      </header>
      <main className="entrance-stages" id="main-content" tabIndex={-1}>
        <FirstBook
          inside={stage === "inside"}
          draft={book ? { name: book.name, profile: book.profile, currency: book.currency } : draft}
          journey={journey}
          createBook={createBook}
          onDone={() => setStage("inside")}
          onOpen={onOpen}
        />
      </main>
    </div>
  );
}

// Stages that aren't showing can't be reached by keyboard or screen readers.
function hidden(when: boolean) {
  return when ? { inert: true } : {};
}

function useFocusWhenShown(shown: boolean) {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    if (!shown) return;
    const id = window.setTimeout(() => ref.current?.querySelector<HTMLElement>("button:not([disabled]), input")?.focus({ preventScroll: true }), 600);
    return () => window.clearTimeout(id);
  }, [shown]);
  return ref;
}

function FirstBook({
  inside,
  draft,
  journey,
  createBook,
  onDone,
  onOpen,
}: {
  inside: boolean;
  draft?: EntranceDraft | null;
  journey: Journey;
  createBook: (book: { name: string; profile: string; currency: string; timezone: string }) => Promise<void>;
  onDone: () => void;
  onOpen?: (page: "dashboard" | "approvals") => void;
}) {
  const heading = useId();
  const [name, setName] = useState(draft?.name ?? "");
  const [profile, setProfile] = useState(draft?.profile ?? "");
  const [currency, setCurrency] = useState(() =>
    draft && bookCurrencyOptions().some((o) => o.value === draft.currency) ? draft.currency : browserBookCurrency(),
  );
  const [zone, setZone] = useState(draft?.timezone || browserTimeZone());
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const ref = useFocusWhenShown(!inside);
  const ready = name.trim() !== "" && profile !== "" && currency !== "" && zone !== "";
  async function create() {
    if (!ready) {
      setError(!profile ? "Choose Personal or Business." : !name.trim() ? "Give your Book a name." : "Choose a currency and timezone.");
      return;
    }
    setError("");
    setCreating(true);
    try {
      await createBook({ name: name.trim(), profile, currency, timezone: zone });
      onDone();
    } catch (e) {
      setCreating(false);
      setError(e instanceof Error && e.message ? e.message : "The Book wasn’t created. Try again.");
    }
  }
  return (
    <section
      ref={ref}
      className="entrance-stage entrance-book"
      data-inside={inside || undefined}
      aria-labelledby={heading}
    >
      <div className="entrance-book-form" {...hidden(inside)}>
        <span className="entrance-eyebrow">Your first Book</span>
        <h1 id={heading}>Every number starts here</h1>
        <PurposeChoice value={profile} onChange={setProfile} />
        <Field label="Book name">
          <input value={name} maxLength={120} onChange={(e) => setName(e.target.value)} placeholder={profile === "business" ? "Your company’s name" : "Your name"} />
        </Field>
        <OnboardingChoice label="Currency" name="currency" value={currency} items={currencyItems} onChange={setCurrency} placeholder="Search currency or country" />
        <AccountingTimezone value={zone} onChange={setZone} />
        <div className="entrance-commit">
          {error && <p className="entrance-error" role="alert">{error}</p>}
          <Button size="lg" busy={creating} onClick={() => void create()}>
            Create Book
          </Button>
          <p className="entrance-fine">Type, currency and timezone can’t change after the Book is created.</p>
        </div>
      </div>
      <JourneyStatus name={name} profile={profile} currency={currency} zone={zone} journey={journey} inside={inside} onOpen={onOpen} />
    </section>
  );
}

type Phase = "statement" | "connect" | "approve" | "working" | "done" | "ready" | "unusable";

/** Inside the new Book: the owner uploads the first statement into Vult, lets
 * their agent in, then watches what Vult sees the agent do with it. Without a
 * statement the Book is simply ready for one. */
function JourneyStatus({
  name,
  profile,
  currency,
  journey,
  inside,
  onOpen,
}: {
  name: string;
  profile: string;
  currency: string;
  zone: string;
  journey: Journey;
  inside: boolean;
  onOpen?: (page: "dashboard" | "approvals") => void;
}) {
  const title = name.trim() || "Your Book";
  const book = useMemo(() => ({ currency, scale: currencyScale(currency) }), [currency]);
  const format = (minor: string) => {
    try {
      return money(minor, book);
    } catch {
      return minor;
    }
  };
  const [phase, setPhase] = useState<Phase>("statement");
  const [agent, setAgent] = useState<EntranceAgent | undefined>(journey.agent);
  const [file, setFile] = useState("");
  const [steps, setSteps] = useState<AgentStatusStep[]>([]);
  const [activity, setActivity] = useState<{ label: string; detail?: string; count?: string }>({ label: "" });
  const [since, setSince] = useState<number>();
  const [entries, setEntries] = useState<EntranceEntry[]>([]);
  const [reviews, setReviews] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [allowed, setAllowed] = useState(false);
  const [unusable, setUnusable] = useState("");
  const [allowError, setAllowError] = useState("");
  const [resumed, setResumed] = useState(!journey.resume);

  // Coming back to the page continues where the owner left off.
  useEffect(() => {
    if (!inside || resumed || !journey.resume) return;
    let live = true;
    journey.resume().then(
      (where) => {
        if (!live) return;
        setResumed(true);
        if (!where) return;
        if (where.agent) setAgent(where.agent);
        setFile(where.file);
        setAllowed(where.allowed);
        if (where.allowed && where.file) void work(where.file);
        else if (where.allowed && where.skipped) setPhase("ready");
        else if (where.file || where.skipped) setPhase(where.agent ? "approve" : "connect");
      },
      () => { if (live) setResumed(true); },
    );
    return () => {
      live = false;
    };
  }, [inside, resumed, journey]);

  // After the statement step, bring in the agent: it's waiting already, or it
  // still has to be connected.
  const next = (uploaded: string) => {
    if (allowed) {
      if (uploaded) void work(uploaded);
      else setPhase("ready");
    } else setPhase(agent ? "approve" : "connect");
  };

  useEffect(() => {
    if (!inside || !resumed || phase !== "connect") return;
    let live = true;
    journey.waitForAgent().then((who) => {
      if (!live) return;
      setAgent(who);
      // On a Vult server the owner lets the agent in on its sign-in page.
      if (journey.connection.kind === "server") {
        setAllowed(true);
        if (file) void work(file);
        else setPhase("ready");
      } else setPhase("approve");
    });
    return () => {
      live = false;
    };
  }, [inside, phase, journey]);

  async function work(document = file) {
    setPhase("working");
    setSteps([]);
    setEntries([]);
    setReviews([]);
    setActivity({ label: "" });
    let declined = "";
    // Each kind of action is one step: it finishes when the agent moves on.
    let kind = "";
    let began = Date.now();
    const accounts: string[] = [];
    const recorded: EntranceEntry[] = [];
    const flagged: string[] = [];
    const finish = () => {
      const took = Date.now() - began;
      const add = (step: Omit<AgentStatusStep, "durationMs">) => setSteps((list) => [...list, { ...step, durationMs: took }]);
      if (kind === "opened") add({ id: "opened", label: `Opened ${document}`, detail: "In Vult" });
      if (kind === "checked") add({ id: "checked", label: "Checked it hasn’t been recorded before" });
      if (kind === "account") add({ id: "accounts", label: `Set up ${accounts.length} ${accounts.length === 1 ? "account" : "accounts"}`, detail: accounts.join(" · ") });
      if (kind === "entry" && recorded.length) {
        const first = recorded[0].date;
        const last = recorded[recorded.length - 1].date;
        add({ id: "entries", label: `Recorded ${recorded.length} entries`, detail: first === last ? first : `${first} – ${last}` });
      }
      if (kind === "review") add({ id: "review", label: `Flagged ${flagged.length} for your review`, detail: flagged.join(" · ") });
      if (kind === "compared") add({ id: "compared", label: "Compared the balance with the statement" });
      if (kind === "balanced") add({ id: "balanced", label: "Checked that everything balances" });
    };
    const start = (next: string) => {
      if (next === kind) return;
      if (kind) finish();
      kind = next;
      began = Date.now();
      setSince(began);
    };
    await journey.watchAgent((event) => {
      start(event.kind);
      if (event.kind === "opened") setActivity({ label: `Reading ${event.document}`, detail: "Opened in Vult" });
      if (event.kind === "checked") setActivity({ label: "Checking it hasn’t been recorded before", detail: "Comparing with your Book’s other statements" });
      if (event.kind === "account") {
        accounts.push(event.name);
        setActivity({ label: "Setting up accounts", detail: event.name, count: String(accounts.length) });
      }
      if (event.kind === "entry") {
        recorded.push(event.entry);
        setEntries([...recorded]);
        const e = event.entry;
        setActivity({
          label: "Recording entries",
          detail: [e.date, e.description, e.amountMinor ? `${BigInt(e.amountMinor) > 0n ? "+" : ""}${format(e.amountMinor)}` : "", event.cited ?? ""].filter(Boolean).join(" · "),
          count: `${recorded.length} recorded`,
        });
      }
      if (event.kind === "review") {
        flagged.push(event.description);
        setReviews([...flagged]);
        setActivity({ label: "Flagging entries for your review", detail: event.description });
      }
      if (event.kind === "compared") setActivity({ label: "Comparing the balance with the statement", detail: "Statement closing balance" });
      if (event.kind === "balanced") setActivity({ label: "Checking that everything balances", detail: "Trial balance" });
      if (event.kind === "unusable") declined = event.reason;
    });
    finish();
    if (declined) {
      setUnusable(declined);
      setPhase("unusable");
      return;
    }
    setPhase("done");
  }

  async function allow() {
    setBusy(true);
    setAllowError("");
    try {
      await journey.approveAgent();
      setAllowed(true);
      if (file) void work();
      else setPhase("ready");
    } catch (e) {
      setAllowError(e instanceof Error && e.message ? e.message : "That didn’t go through. Try again.");
    } finally {
      setBusy(false);
    }
  }

  if (!inside || !resumed) return null;
  const latest = entries[entries.length - 1];
  const waiting = entries.filter((e) => e.review).length;
  return (
    <div className="entrance-journey">
      <div className="entrance-journey-head">
        <span className="entrance-eyebrow">{profile === "business" ? "Business" : "Personal"} Book · {currency}</span>
        <h2>{title}</h2>
      </div>
      {phase === "statement" && (
        <div className="entrance-guide">
          <div className="entrance-guide-head">
            <strong>Start with a statement</strong>
            <small>Upload your latest bank or card statement. Your agent reads it here.</small>
          </div>
          <FirstStatement
            onAdded={async (f) => {
              await journey.addStatement(f);
              setFile(f.name);
              next(f.name);
            }}
            onSkip={() => {
              journey.skipStatement?.();
              next("");
            }}
          />
        </div>
      )}
      {phase === "connect" && (
        <div className="entrance-guide">
          <div className="entrance-guide-head">
            <strong>Connect your agent</strong>
            <small>{file ? `It records ${file} into ${title}. You let it in once, here.` : "Your agent records your statements. You let it in once, here."}</small>
          </div>
          <ConnectAgent connection={journey.connection} />
          <p className="entrance-waiting">
            <span className="entrance-live" aria-hidden="true" /> Waiting for your agent…
          </p>
        </div>
      )}
      {phase === "approve" && agent && (
        <div className="entrance-guide entrance-guide-row" data-arrived>
          <span className="entrance-agent-avatar">{agent.slice(0, 1)}</span>
          <span className="entrance-agent-text">
            <strong>{agent} wants to come in</strong>
            <small>{file ? `To record ${file}. ` : ""}Anything it isn’t sure about waits for you.</small>
          </span>
          <Button busy={busy} onClick={() => void allow()}>
            Allow
          </Button>
          {allowError && <p className="entrance-error" role="alert">{allowError}</p>}
        </div>
      )}
      {phase === "ready" && (
        <div className="entrance-guide entrance-guide-row">
          <span className="entrance-agent-text">
            <strong>{title} is ready</strong>
            <small>Upload your first statement whenever you like{agent ? `; ${agent} will record it` : ""}.</small>
          </span>
          <Button color="secondary" onClick={() => onOpen?.("dashboard")}>Open {title}</Button>
        </div>
      )}
      {phase === "unusable" && (
        <AgentStatus
          steps={steps}
          label={`${agent ?? "Your agent"} couldn’t use ${file}`}
          detail={unusable}
          state="paused"
          action={
            <Button
              color="secondary"
              onClick={() => {
                setFile("");
                setSteps([]);
                setPhase("statement");
              }}
            >
              Upload another
            </Button>
          }
        >
          <p className="entrance-fine">Nothing was recorded. The file stays in Vult, where you can remove it.</p>
        </AgentStatus>
      )}
      {(phase === "working" || phase === "done") && (
        <AgentStatus
          steps={steps}
          maxSteps={6}
          label={
            phase === "done"
              ? `${entries.length + reviews.length} ${entries.length + reviews.length === 1 ? "entry" : "entries"} from ${file}`
              : activity.label || `Waiting for ${agent ?? "your agent"} to start`
          }
          detail={
            phase === "done"
              ? reviews.length
                ? `${entries.length} recorded · ${reviews.length} waiting for your review`
                : `${entries.length} recorded`
              : activity.label
                ? activity.detail
                : `It reads ${file} as soon as it begins`
          }
          state={phase === "done" ? "done" : activity.label ? "working" : "waiting"}
          since={since}
          action={
            phase === "done" ? (
              <Button color="secondary" onClick={() => onOpen?.(reviews.length ? "approvals" : "dashboard")}>{reviews.length ? "Review" : "Open"}</Button>
            ) : activity.count ? (
              <span className="entrance-count">{activity.count}</span>
            ) : undefined
          }
        />
      )}
    </div>
  );
}

function CopyLine({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="entrance-copy">
      <code>{text}</code>
      <Button
        color="tertiary"
        size="sm"
        aria-label={copied ? "Copied" : label}
        title={copied ? "Copied" : label}
        onClick={() => void navigator.clipboard?.writeText(text).then(() => setCopied(true), () => undefined)}
      >
        {copied ? <Check aria-hidden="true" /> : <Copy01 aria-hidden="true" />}
      </Button>
    </div>
  );
}

/** One command adds Vult to the agents it finds on this computer. */
function ConnectAgent({ connection }: { connection: EntranceConnection }) {
  const local = connection.kind === "local";
  const steps: { text: string; command?: string }[] = local
    ? [
        { text: "Run this in Terminal to add Vult to your agents:", command: `${connection.program} mcp install` },
        { text: "Restart your agent and ask it: “Use Vult to do my bookkeeping.”" },
      ]
    : [
        { text: "Add Vult to your agent as an MCP server:", command: connection.address },
        { text: "Your agent asks you to sign in. Then ask it: “Use Vult to do my bookkeeping.”" },
      ];
  return (
    <div className="entrance-guide-step">
      <ol className="entrance-steps">
        {steps.map((s, i) => (
          <li key={i}>
            <span>{s.text}</span>
            {s.command && <CopyLine text={s.command} label={local ? "Copy command" : "Copy address"} />}
          </li>
        ))}
      </ol>
    </div>
  );
}

/** The first statement goes into Vult, not into the agent's chat. */
// What Vult itself can check before the agent sees the file.
const statementTypes = ["application/pdf", "text/csv", "image/jpeg", "image/png", "image/webp"];
const maxStatementBytes = 32 * 1024 * 1024;

function FirstStatement({ onAdded, onSkip }: { onAdded: (file: File) => Promise<void>; onSkip: () => void }) {
  const input = useId();
  const [dragging, setDragging] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function take(file?: File) {
    if (!file) return;
    const type = file.type || (file.name.toLowerCase().endsWith(".csv") ? "text/csv" : "");
    if (!statementTypes.includes(type)) {
      setError(`${file.name} isn’t a PDF, CSV or photo. Statements usually come as a PDF or CSV from your bank.`);
      return;
    }
    if (file.size > maxStatementBytes) {
      setError(`${file.name} is larger than 32 MB.`);
      return;
    }
    setError("");
    setBusy(true);
    try {
      await onAdded(file);
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="entrance-guide-step">
      <label
        htmlFor={input}
        className="entrance-drop"
        data-dragging={dragging || undefined}
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          void take(e.dataTransfer.files[0]);
        }}
      >
        <UploadCloud02 aria-hidden="true" />
        <strong>{busy ? "Uploading…" : "Drop one statement here"}</strong>
        <small>PDF, CSV or a photo. Add the rest after you’ve reviewed this one.</small>
        <input id={input} type="file" accept=".pdf,.csv,application/pdf,text/csv,image/jpeg,image/png,image/webp" onChange={(e) => void take(e.target.files?.[0])} />
      </label>
      {error && <p className="entrance-error" role="alert">{error}</p>}
      <Button color="tertiary" size="sm" className="entrance-skip" onClick={onSkip}>
        Skip for now
      </Button>
    </div>
  );
}

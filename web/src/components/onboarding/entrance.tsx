import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Check, Copy01, Fingerprint, Lock01, UploadCloud02 } from "../../design-system/icons";
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
import { PurposeChoice, currencyItems } from "./welcome";
import "./onboarding.css";
import "./entrance.css";

type Stage = "threshold" | "keys" | "book" | "inside";

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

/** How agents reach this Vult: through the vult command on this Mac, or by
 * address on a Vult server. */
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
  /** Offers an authenticator app instead of a passkey (a Vult server). */
  authenticator?: {
    secret: string;
    /** A QR code for the secret, as an image the page can show. */
    qr?: string;
    verify: (code: string) => Promise<void>;
  };
};

/** The first minutes in a new Vult, as one continuous scene: create the
 * passkey, keep the recovery codes, shape the first Book, then step inside it
 * and bring in the agent and the first statement. It works the same whether
 * the owner started from their agent's chat or from the browser. */
export function Entrance({
  start = "threshold",
  draft,
  book,
  recoveryCodes = [],
  createPasskey = async () => undefined,
  createBook,
  journey,
  onOpen,
}: {
  /** Where to begin. On this Mac the passkey and recovery codes come first on
   * Vult's sign-in pages, so the scene begins at the Book. */
  start?: Stage;
  draft?: EntranceDraft | null;
  /** A Book created on an earlier visit, to continue inside it. */
  book?: { name: string; profile?: string; currency: string };
  recoveryCodes?: string[];
  createPasskey?: () => Promise<void>;
  createBook: (book: { name: string; profile: string; currency: string; timezone: string }) => Promise<void>;
  journey: Journey;
  /** Leaves the scene for the Book, or for the entries waiting for review. */
  onOpen?: (page: "dashboard" | "approvals") => void;
}) {
  const [stage, setStage] = useState<Stage>(start);
  const order: Stage[] = ["threshold", "keys", "book", "inside"];
  const at = order.indexOf(stage);
  const state = (s: Stage) => (s === stage ? "on" : order.indexOf(s) < at ? "past" : "next");
  return (
    <div className="onboarding entrance" data-stage={stage}>
      <OnboardingParticles className="onboarding-particles" aria-hidden="true" color="#666666" ease={20} quantity={140} />
      <header className="entrance-header">
        <VultBrand />
      </header>
      <main className="entrance-stages" id="main-content" tabIndex={-1}>
        {order.indexOf(start) <= 0 && <Threshold
          state={state("threshold")}
          onDone={() => setStage("keys")}
          createPasskey={createPasskey}
          authenticator={journey.authenticator}
          hosted={journey.connection.kind === "server"}
        />}
        {order.indexOf(start) <= 1 && <Keys state={state("keys")} codes={recoveryCodes} onDone={() => setStage("book")} />}
        <FirstBook
          state={state("book")}
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

function useFocusWhenShown(state: string) {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    if (state !== "on") return;
    const id = window.setTimeout(() => ref.current?.querySelector<HTMLElement>("button:not([disabled]), input")?.focus({ preventScroll: true }), 600);
    return () => window.clearTimeout(id);
  }, [state]);
  return ref;
}

function Threshold({
  state,
  onDone,
  createPasskey,
  authenticator,
  hosted,
}: {
  state: string;
  onDone: () => void;
  createPasskey: () => Promise<void>;
  authenticator?: Journey["authenticator"];
  hosted: boolean;
}) {
  const heading = useId();
  const [phase, setPhase] = useState<"idle" | "reading" | "open">("idle");
  const [useApp, setUseApp] = useState(false);
  const [error, setError] = useState("");
  const ref = useFocusWhenShown(state);
  async function start() {
    setError("");
    setPhase("reading");
    try {
      await createPasskey();
      setPhase("open");
      window.setTimeout(onDone, 900);
    } catch {
      setPhase("idle");
      setError("The passkey wasn’t created. Try again.");
    }
  }
  return (
    <section ref={ref} className="entrance-stage entrance-threshold" data-state={state} aria-labelledby={heading} {...hidden(state !== "on")}>
      <div className="entrance-dial" data-phase={phase} aria-hidden="true">
        <svg viewBox="0 0 228 228">
          <g className="entrance-ring-outer">
            <circle cx="114" cy="114" r="110" />
            <circle className="entrance-ticks" cx="114" cy="114" r="110" />
          </g>
          <g className="entrance-ring-inner">
            <circle className="entrance-dash" cx="114" cy="114" r="88" />
            <circle className="entrance-dots" cx="114" cy="114" r="98" />
          </g>
        </svg>
        <span className="entrance-core">
          {phase === "open" ? <Check /> : <Fingerprint />}
        </span>
      </div>
      <h1 id={heading}>Your Vult is ready</h1>
      <p className="entrance-lede">{hosted ? "We host it for you, and only you can open it." : "It lives on this Mac, and only you can open it."}</p>
      {useApp && authenticator ? (
        <AuthenticatorSetup
          authenticator={authenticator}
          onDone={() => {
            setPhase("open");
            window.setTimeout(onDone, 900);
          }}
          onBack={() => setUseApp(false)}
        />
      ) : (
        <>
          <Button size="lg" onClick={() => void start()} disabled={phase !== "idle"}>
            <Fingerprint aria-hidden="true" /> Create your passkey
          </Button>
          <p className="entrance-fine">Touch ID or your device password. Nothing to remember.</p>
          {authenticator && phase === "idle" && (
            <Button color="tertiary" size="sm" onClick={() => setUseApp(true)}>
              Use an authenticator app instead
            </Button>
          )}
        </>
      )}
      <p className="entrance-status" role="status" aria-live="polite">
        {error || (phase === "reading" ? "Touch the sensor to create your passkey…" : phase === "open" ? "Passkey created" : "")}
      </p>
    </section>
  );
}

/** For owners who can't or won't use a passkey: a six-digit code from an
 * authenticator app. Recovery codes follow either way. */
function AuthenticatorSetup({
  authenticator,
  onDone,
  onBack,
}: {
  authenticator: NonNullable<Journey["authenticator"]>;
  onDone: () => void;
  onBack: () => void;
}) {
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function verify() {
    if (!/^\d{6}$/.test(code)) {
      setError("Enter the 6-digit code from your app.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await authenticator.verify(code);
      onDone();
    } catch {
      setBusy(false);
      setError("That code didn’t match. Try the next one your app shows.");
    }
  }
  return (
    <form
      className="entrance-authenticator"
      onSubmit={(e) => {
        e.preventDefault();
        void verify();
      }}
    >
      <p className="entrance-lede">Scan this with your authenticator app, then enter the code it shows.</p>
      {authenticator.qr ? <img className="entrance-qr" src={authenticator.qr} alt="QR code for your authenticator app" /> : null}
      <p className="entrance-fine">
        Can’t scan? Enter this key: <code>{authenticator.secret}</code>
      </p>
      <Field label="6-digit code" error={error}>
        <input inputMode="numeric" autoComplete="one-time-code" maxLength={6} value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))} />
      </Field>
      <Button size="lg" type="submit" busy={busy}>
        Verify
      </Button>
      <Button color="tertiary" size="sm" onClick={onBack}>
        Use a passkey instead
      </Button>
    </form>
  );
}

function Keys({ state, codes, onDone }: { state: string; codes: string[]; onDone: () => void }) {
  const heading = useId();
  const [saved, setSaved] = useState(false);
  const [copied, setCopied] = useState(false);
  const ref = useFocusWhenShown(state);
  return (
    <section ref={ref} className="entrance-stage entrance-keys" data-state={state} aria-labelledby={heading} {...hidden(state !== "on")}>
      <span className="entrance-eyebrow"><Lock01 aria-hidden="true" /> Your recovery codes</span>
      <h1 id={heading}>If you lose your passkey, these let you back in</h1>
      <p className="entrance-lede">Each code works once. Keep them somewhere safe and offline, like a password manager. Never give them to an agent.</p>
      <ol className="entrance-codes">
        {codes.map((code, i) => (
          <li key={code} style={{ animationDelay: `${300 + i * 50}ms` }}>
            <span>{String(i + 1).padStart(2, "0")}</span>
            <code>{code}</code>
          </li>
        ))}
      </ol>
      <div className="entrance-row">
        <Button
          color="secondary"
          onClick={() => void navigator.clipboard?.writeText(codes.join("\n")).then(() => setCopied(true), () => undefined)}
        >
          {copied ? <Check aria-hidden="true" /> : <Copy01 aria-hidden="true" />} {copied ? "Copied" : "Copy codes"}
        </Button>
        <label className="entrance-check">
          <input type="checkbox" checked={saved} onChange={(e) => setSaved(e.target.checked)} /> I’ve saved them
        </label>
      </div>
      <Button size="lg" disabled={!saved} onClick={onDone}>
        Continue
      </Button>
    </section>
  );
}

function FirstBook({
  state,
  inside,
  draft,
  journey,
  createBook,
  onDone,
  onOpen,
}: {
  state: string;
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
  const ref = useFocusWhenShown(state);
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
      data-state={inside ? "on" : state}
      data-inside={inside || undefined}
      aria-labelledby={heading}
      {...hidden(state !== "on" && !inside)}
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
        {zone === browserTimeZone() && (
          <p className="entrance-fine entrance-field-note">
            From {journey.connection.kind === "local" ? "this Mac’s" : "your device’s"} clock
          </p>
        )}
        <p className="entrance-fine">A Book’s currency can’t change after it’s created.</p>
        {error && <p className="entrance-error" role="alert">{error}</p>}
        <Button size="lg" busy={creating} onClick={() => void create()}>
          Create Book
        </Button>
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
      setPhase("approve");
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
            <strong>Do you have a statement to start with?</strong>
            <small>Upload your most recent bank or card statement. It stays in Vult; your agent reads it here once you let it in.</small>
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
            <span className="entrance-live" aria-hidden="true" /> Waiting for Claude Code or Codex…
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

const agents: EntranceAgent[] = ["Claude Code", "Codex"];

/** One command connects Claude Code and Codex on this Mac; on a Vult server
 * each agent adds the address and signs in. The page notices when it arrives. */
function ConnectAgent({ connection }: { connection: EntranceConnection }) {
  const [agent, setAgent] = useState<EntranceAgent>("Claude Code");
  const tabs = useId();
  const local = connection.kind === "local";
  const steps: { text: string; command?: string }[] = local
    ? [
        { text: "Run this in Terminal. It connects Claude Code and Codex, whichever you have.", command: `${connection.program} mcp install` },
        agent === "Claude Code" ? { text: "Restart Claude Code, or type /mcp in it and reconnect Vult." } : { text: "Restart Codex." },
        { text: `Ask ${agent}: “Use Vult to do my bookkeeping.”` },
      ]
    : agent === "Claude Code"
      ? [
          { text: "Run this in Terminal:", command: `claude mcp add --transport http vult ${connection.address}` },
          { text: "In Claude Code, type /mcp, choose Vult and sign in." },
        ]
      : [
          { text: "Run this in Terminal:", command: `codex mcp add vult --url ${connection.address}` },
          { text: "Then sign in to Vult:", command: "codex mcp login vult" },
        ];
  return (
    <div className="entrance-guide-step">
      <div className="welcome-agent-tabs" role="radiogroup" aria-label="Your agent">
        {agents.map((a) => (
          <label key={a} data-selected={agent === a || undefined}>
            <input type="radio" name={tabs} checked={agent === a} onChange={() => setAgent(a)} />
            {a}
          </label>
        ))}
      </div>
      <ol className="entrance-steps">
        {steps.map((s, i) => (
          <li key={i}>
            <span>{s.text}</span>
            {s.command && <CopyLine text={s.command} label="Copy command" />}
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

import { useEffect, useState, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { FailureState } from "../../src/components/ui/failure-state";
import { EmptyState } from "../../src/components/ui/empty-state";
import { LoadingState } from "../../src/components/ui/loading-state";
import { Notice } from "../../src/components/ui/notice";
import {
  ConfirmAction,
  ConfirmDetail,
  ConfirmFooter,
} from "../../src/components/ui/confirm-action";
import { File06, Wallet01 } from "../../src/design-system/icons";
import { ErrorNotice } from "../../src/components/ui/error-notice";
import { Field } from "../../src/components/form";
import { Button } from "../../src/components/ui/button";
import {
  APIError,
  errorMessage,
  presentFailure,
  responseFailure,
  type FailureContext,
} from "../../src/lib/errors";
import { parseTransactionJournal } from "../../src/lib/transaction-journal";
import { ErrorCatalog } from "./error-catalog";
import "../../src/style.css";
import "./states.css";

// Synthetic only: no requests, browser storage, owner records or postings.
// Every specimen is a real component, and every failure runs through the
// shared policy, so the gallery shows exactly what the application shows.

type Scope = "page" | "region" | "inline";
type Case = {
  id: string;
  label: string;
  source: string;
  error: unknown;
  context: FailureContext;
  retry?: boolean;
  review?: boolean;
  title?: string;
  scopes: Scope[];
};

function integrityFailure() {
  try {
    parseTransactionJournal(
      "book_id,currency,transaction_id,line_id,account_code,side,amount_minor\npreview,USD,example,line1,cash,debit,1\n",
      { id: "preview", currency: "USD" },
    );
  } catch (error) {
    return error;
  }
}

const reads: Scope[] = ["page", "region", "inline"];
const cases: Case[] = [
  {
    id: "connection",
    label: "Connection lost",
    source: "Network error · read",
    error: responseFailure(0, {}, "read"),
    context: { intent: "read", resource: "dashboard" },
    retry: true,
    scopes: reads,
  },
  {
    id: "unavailable",
    label: "Service unavailable",
    source: "HTTP 503 · read",
    error: responseFailure(503, {}, "read"),
    context: { intent: "read", resource: "dashboard" },
    retry: true,
    scopes: reads,
  },
  {
    id: "busy",
    label: "Too many requests",
    source: "HTTP 429 · read",
    error: responseFailure(429, {}, "read"),
    context: { intent: "read", resource: "activity" },
    retry: true,
    scopes: reads,
  },
  {
    id: "unsupported",
    label: "View not available yet",
    source: "HTTP 501 FEATURE_UNAVAILABLE · read",
    error: responseFailure(501, { code: "FEATURE_UNAVAILABLE" }, "read"),
    context: { intent: "read", resource: "statement" },
    scopes: reads,
  },
  {
    id: "missing",
    label: "Not found",
    source: "HTTP 404 · read",
    error: responseFailure(404, {}, "read"),
    context: { intent: "read", resource: "document" },
    scopes: reads,
  },
  {
    id: "session-ended",
    label: "Session ended",
    source: "HTTP 401 · read",
    error: responseFailure(401, {}, "read"),
    context: { intent: "read", resource: "accounts" },
    scopes: reads,
  },
  {
    id: "signed-out",
    label: "Signed out",
    source: "HTTP 401 SIGNED_OUT · read",
    error: responseFailure(401, { code: "SIGNED_OUT" }, "read"),
    context: { intent: "read", resource: "accounts" },
    scopes: reads,
  },
  {
    id: "no-access",
    label: "No access",
    source: "HTTP 403 · read",
    error: responseFailure(403, {}, "read"),
    context: { intent: "read", resource: "settings" },
    scopes: reads,
  },
  {
    id: "unverified",
    label: "Records failed verification",
    source: "Verified integrity check · read",
    error: integrityFailure(),
    context: { intent: "read", resource: "statement" },
    retry: true,
    review: true,
    scopes: reads,
  },
  {
    id: "uncertain",
    label: "Save outcome unknown",
    source: "HTTP 504 · write",
    error: responseFailure(504, {}, "write"),
    context: { intent: "write", retained: "form" },
    review: true,
    scopes: ["inline"],
  },
  {
    id: "conflict",
    label: "Changed elsewhere",
    source: "HTTP 409 · write",
    error: responseFailure(409, {}, "write"),
    context: { intent: "write", retained: "form" },
    review: true,
    scopes: ["inline"],
  },
  {
    id: "rejected",
    label: "Rejected change",
    source: "HTTP 422 · write",
    error: responseFailure(422, {}, "write"),
    context: { intent: "write", retained: "form" },
    scopes: ["inline"],
  },
];

const severities = ["routine", "attention", "critical"] as const;
const recoveries = ["retry", "sign-in", "confirm", "review", "none"] as const;
const recoveryLabel = {
  retry: "Try again",
  "sign-in": "Sign in",
  confirm: "Continue",
  review: "Review",
  none: "No action",
};
const scopeNote: Record<Scope, string> = {
  page: "A whole screen couldn’t render",
  region: "One panel couldn’t load",
  inline: "Beside the action that failed",
};
const pause = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

function Recoverable({
  children,
}: {
  children: (onDone: () => Promise<void>) => ReactNode;
}) {
  const [done, setDone] = useState(false);
  if (done)
    return (
      <div className="gallery-recovered">
        <p className="vult-type-ui">Loaded.</p>
        <Button color="tertiary" size="sm" onClick={() => setDone(false)}>
          Show failure again
        </Button>
      </div>
    );
  return (
    <>
      {children(async () => {
        await pause(700);
        setDone(true);
      })}
    </>
  );
}

function Specimen({ example, scope }: { example: Case; scope: Scope }) {
  if (!example.scopes.includes(scope))
    return (
      <div className="gallery-cell gallery-cell-unused">
        <p className="vult-type-metadata">
          Not used. Write failures stay beside the form that sent them.
        </p>
      </div>
    );
  return (
    <Recoverable>
      {(recover) => {
        const handlers = {
          onRetry: example.retry ? recover : undefined,
          onReview: example.review ? recover : undefined,
        };
        if (scope === "inline")
          return (
            <div className="gallery-cell gallery-inline">
              <ErrorNotice
                error={example.error}
                {...example.context}
                {...handlers}
              />
            </div>
          );
        return (
          <div className={`gallery-cell gallery-${scope}`}>
            {scope === "region" && (
              <p className="gallery-region-heading vult-type-label">Panel</p>
            )}
            <FailureState
              scope={scope}
              error={example.error}
              title={example.title}
              {...example.context}
              {...handlers}
            />
          </div>
        );
      }}
    </Recoverable>
  );
}

function Chip({ children, tone }: { children: ReactNode; tone?: string }) {
  return (
    <span className="gallery-chip vult-type-metadata" data-tone={tone}>
      {children}
    </span>
  );
}

function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: readonly { value: T; label: string }[];
  onChange: (value: T) => void;
}) {
  return (
    <div className="gallery-segmented" role="radiogroup" aria-label={label}>
      <span className="vult-type-metadata gallery-segmented-label">
        {label}
      </span>
      <div>
        {options.map((option) => (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={value === option.value}
            className="vult-type-label"
            onClick={() => onChange(option.value)}
          >
            {option.label}
          </button>
        ))}
      </div>
    </div>
  );
}

type Section =
  | "all"
  | "failure"
  | "loading"
  | "empty"
  | "lifecycle"
  | "notice"
  | "confirm";
const scopeName = (scope: Scope) =>
  scope === "page" ? "Page" : scope === "region" ? "Region" : "Inline";

function CaseFrame({
  id,
  title,
  chips,
  children,
}: {
  id: string;
  title: string;
  chips?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="gallery-case" aria-labelledby={`case-${id}`}>
      <header className="gallery-case-header">
        <h3 id={`case-${id}`} className="vult-type-compact-title">
          {title}
        </h3>
        {chips && <div className="gallery-chips">{chips}</div>}
      </header>
      {children}
    </section>
  );
}

function ScopeGrid({
  render,
}: {
  render: (scope: Scope) => ReactNode | null;
}) {
  return (
    <div className="gallery-grid">
      {(["page", "region", "inline"] as const).map((scope) => {
        const content = render(scope);
        return (
          <div key={scope} className="gallery-column">
            <p className="vult-type-metadata gallery-muted">
              {scopeName(scope)}
            </p>
            {content === null ? (
              <div className="gallery-cell gallery-cell-unused">
                <p className="vult-type-metadata">Not used at this scope.</p>
              </div>
            ) : scope === "inline" ? (
              <div className="gallery-cell gallery-inline">{content}</div>
            ) : (
              <div className={`gallery-cell gallery-${scope}`}>
                {scope === "region" && (
                  <p className="gallery-region-heading vult-type-label">
                    Panel
                  </p>
                )}
                {content}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function SectionHeader({
  title,
  rules,
}: {
  title: string;
  rules: [string, string][];
}) {
  return (
    <header className="gallery-section-header">
      <h2 className="vult-type-section-title">{title}</h2>
      <dl className="gallery-rules">
        {rules.map(([term, rule]) => (
          <div key={term}>
            <dt className="vult-type-label">{term}</dt>
            <dd className="vult-type-metadata gallery-muted">{rule}</dd>
          </div>
        ))}
      </dl>
    </header>
  );
}

function FailureSection({
  severity,
  recovery,
}: {
  severity: "all" | (typeof severities)[number];
  recovery: "all" | (typeof recoveries)[number];
}) {
  const rows = cases
    .map((example) => ({
      example,
      failure: presentFailure(example.error, example.context),
    }))
    .filter(
      ({ failure }) =>
        (severity === "all" || failure.severity === severity) &&
        (recovery === "all" || failure.recovery === recovery),
    );
  return (
    <section className="gallery-section" aria-label="Failure">
      <SectionHeader
        title="Failure"
        rules={[
          ["Page", scopeNote.page],
          ["Region", scopeNote.region],
          ["Inline", scopeNote.inline],
          ["Field", "Validation under the input it describes"],
          ["Title", "Couldn’t + verb + what failed. No full stop."],
          ["Line", "Reason or next step. Never repeats the button or guesses."],
        ]}
      />
      {rows.length === 0 && (
        <p className="vult-type-ui gallery-muted">
          The policy never produces this combination.
        </p>
      )}
      {rows.map(({ example, failure }) => (
        <CaseFrame
          key={example.id}
          id={example.id}
          title={example.label}
          chips={
            <>
              <Chip tone={failure.severity}>{failure.severity}</Chip>
              <Chip>{recoveryLabel[failure.recovery]}</Chip>
              <Chip>{example.source}</Chip>
            </>
          }
        >
          <div className="gallery-grid">
            {(["page", "region", "inline"] as const).map((scope) => (
              <div key={scope} className="gallery-column">
                <p className="vult-type-metadata gallery-muted">
                  {scopeName(scope)}
                </p>
                <Specimen example={example} scope={scope} />
              </div>
            ))}
          </div>
        </CaseFrame>
      ))}
      {(severity === "all" || severity === "attention") &&
        (recovery === "all" || recovery === "review") && (
          <CaseFrame
            id="field"
            title="Field validation"
            chips={
              <>
                <Chip tone="attention">attention</Chip>
                <Chip>Review</Chip>
                <Chip>Local validation</Chip>
              </>
            }
          >
            <div className="gallery-cell gallery-field">
              <Field
                label="Amount"
                error={errorMessage(
                  new APIError(
                    "Enter a positive decimal amount without separators.",
                    400,
                  ),
                )}
              >
                <input defaultValue="1,250.00" />
              </Field>
            </div>
          </CaseFrame>
        )}
      <section className="gallery-case" aria-label="Inline copy">
        <ErrorCatalog />
      </section>
    </section>
  );
}

const loadingCases: { id: string; title: string; labels: Record<Scope, string> }[] = [
  {
    id: "opening",
    title: "Opening a place",
    labels: {
      page: "Opening Example Studio…",
      region: "Opening Example Studio…",
      inline: "Opening secure payment…",
    },
  },
  {
    id: "loading",
    title: "Loading data",
    labels: {
      page: "Opening Books…",
      region: "Loading transactions…",
      inline: "Loading entry…",
    },
  },
  {
    id: "checking",
    title: "Checking or searching",
    labels: {
      page: "Opening Example Studio…",
      region: "Loading report…",
      inline: "Checking the connection…",
    },
  },
];

function LoadingSection() {
  return (
    <section className="gallery-section" aria-label="Loading">
      <SectionHeader
        title="Loading"
        rules={[
          ["Page", "Stands in for a screen; its failure appears in the same place"],
          ["Region", "Stands in for a table, list or panel"],
          ["Inline", "One line beside the content it waits for"],
          ["Timing", "Appears after 240 ms, so fast reads never flash"],
          [
            "Verbs",
            "Opening a Book or workspace, Loading data, Checking a verification, Searching a query, Saving a write",
          ],
          ["Copy", "One phrase ending in an ellipsis. No ‘Reading’, no ‘Please wait’."],
        ]}
      />
      {loadingCases.map((example) => (
        <CaseFrame key={example.id} id={`loading-${example.id}`} title={example.title}>
          <ScopeGrid
            render={(scope) => (
              <LoadingState scope={scope} label={example.labels[scope]} />
            )}
          />
        </CaseFrame>
      ))}
    </section>
  );
}

function EmptySection() {
  return (
    <section className="gallery-section" aria-label="Empty">
      <SectionHeader
        title="Empty"
        rules={[
          ["Nothing yet", "Icon, “No … yet”, and the first step as a sentence or action"],
          ["No matches", "No icon, “No matching …”, and how to widen the search"],
          ["Scoped", "No icon, “No … in this period/view”, and context if it helps"],
          ["Prompt", "Icon, “Select a …”, when a panel waits for a choice"],
          ["Inline", "One quiet line inside a panel, list or field"],
          ["Copy", "Titles have no full stop. Details are sentences."],
        ]}
      />
      <CaseFrame id="empty-new" title="Nothing yet">
        <ScopeGrid
          render={(scope) => (
            <EmptyState
              scope={scope}
              icon={scope === "inline" ? undefined : File06}
              title="No transactions yet"
              detail="Add a transaction from Manage."
              action={
                scope === "inline" ? undefined : (
                  <Button color={scope === "page" ? "primary" : "secondary"} size={scope === "page" ? "md" : "sm"}>
                    Add transaction
                  </Button>
                )
              }
            />
          )}
        />
      </CaseFrame>
      <CaseFrame id="empty-filtered" title="No matches">
        <ScopeGrid
          render={(scope) =>
            scope === "page" ? null : (
              <EmptyState
                scope={scope}
                title="No matching transactions"
                detail="Adjust the search or filters."
              />
            )
          }
        />
      </CaseFrame>
      <CaseFrame id="empty-scoped" title="Scoped to a period or view">
        <ScopeGrid
          render={(scope) =>
            scope === "page" ? null : (
              <EmptyState
                scope={scope}
                title="No postings in this period"
                detail={
                  scope === "inline"
                    ? undefined
                    : "Opening and closing balances still include earlier recorded activity."
                }
              />
            )
          }
        />
      </CaseFrame>
      <CaseFrame id="empty-prompt" title="Waiting for a choice">
        <ScopeGrid
          render={(scope) =>
            scope === "region" ? (
              <EmptyState
                scope="region"
                icon={Wallet01}
                title="Select an account"
                detail="Choose an account to inspect its balance and recorded activity."
              />
            ) : null
          }
        />
      </CaseFrame>
    </section>
  );
}

/** Loading, failure, retry and content in one place, as a person sees them. */
function Lifecycle({ scope }: { scope: "page" | "region" }) {
  const [phase, setPhase] = useState<"loading" | "failed" | "loaded">("loading");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (phase !== "loading") return;
    const timer = setTimeout(
      () => setPhase(attempt === 0 ? "failed" : "loaded"),
      1200,
    );
    return () => clearTimeout(timer);
  }, [phase, attempt]);
  return (
    <div className="gallery-lifecycle">
      <div className={`gallery-cell gallery-${scope}`}>
        {scope === "region" && (
          <p className="gallery-region-heading vult-type-label">Transactions</p>
        )}
        {phase === "loading" ? (
          <LoadingState
            scope={scope}
            label={scope === "page" ? "Opening Example Studio…" : "Loading transactions…"}
          />
        ) : phase === "failed" ? (
          <FailureState
            scope={scope}
            error={responseFailure(503, {}, "read")}
            title={scope === "page" ? "Couldn’t open Example Studio" : "Couldn’t load transactions"}
            onRetry={async () => {
              setAttempt((value) => value + 1);
              setPhase("loading");
            }}
          />
        ) : (
          <EmptyState
            scope={scope}
            icon={File06}
            title="No transactions yet"
            detail="Add a transaction from Manage."
          />
        )}
      </div>
      <Button
        color="tertiary"
        size="sm"
        onClick={() => {
          setAttempt(0);
          setPhase("loading");
        }}
      >
        Replay
      </Button>
    </div>
  );
}

function LifecycleSection() {
  return (
    <section className="gallery-section" aria-label="Lifecycle">
      <SectionHeader
        title="Lifecycle"
        rules={[
          ["Order", "Loading, then failure or content, in the same place and size"],
          ["Retry", "Try again returns to loading, so the person sees it working"],
          ["First try", "Fails here on purpose; the retry succeeds"],
        ]}
      />
      <CaseFrame id="lifecycle" title="Open, fail, try again, load">
        <div className="gallery-grid gallery-grid-two">
          <div className="gallery-column">
            <p className="vult-type-metadata gallery-muted">Page</p>
            <Lifecycle scope="page" />
          </div>
          <div className="gallery-column">
            <p className="vult-type-metadata gallery-muted">Region</p>
            <Lifecycle scope="region" />
          </div>
        </div>
      </CaseFrame>
    </section>
  );
}

function NoticeSection() {
  const [floating, setFloating] = useState(false);
  return (
    <section className="gallery-section" aria-label="Notices">
      <SectionHeader
        title="Notices"
        rules={[
          ["Success", "A change is saved. Past tense, then where it went or what stays: “Saved to the Book.”"],
          ["Info", "Something worth knowing that needs no action."],
          ["Caution", "A condition to respect before acting, and how to change it. Read with the page, not announced."],
          ["Placement", "Inline next to what changed. Floating only for changes made in a dialog that has closed."],
          ["Copy", "One or two sentences, each ending with a full stop. No exclamation marks."],
          ["Failures", "Never a notice. Use the failure states."],
        ]}
      />
      <CaseFrame id="notice-tones" title="Tones">
        <div className="gallery-stack">
          <Notice tone="success">Saved to the Book.</Notice>
          <Notice tone="success">Access updated.</Notice>
          <Notice tone="info">
            Latest details loaded. Changes are kept; review them before
            saving.
          </Notice>
          <Notice tone="caution">
            This period is closed. Reopen it in Settings to change its entries.
          </Notice>
        </div>
      </CaseFrame>
      <CaseFrame id="notice-actions" title="With an action or dismiss">
        <div className="gallery-stack">
          <Notice
            tone="success"
            action={
              <Button color="tertiary" size="sm">
                Open review
              </Button>
            }
          >
            Proposal saved. Review it in Statements.
          </Notice>
          <Notice tone="success" onDismiss={() => undefined}>
            File deleted.
          </Notice>
        </div>
      </CaseFrame>
      <CaseFrame id="notice-floating" title="Floating">
        <div className="gallery-recovered">
          <p className="vult-type-metadata gallery-muted">
            After a dialog closes, the outcome floats at the bottom of the
            window until dismissed.
          </p>
          <Button color="secondary" size="sm" onClick={() => setFloating(true)}>
            Show floating notice
          </Button>
        </div>
        {floating && (
          <Notice
            tone="success"
            placement="floating"
            onDismiss={() => setFloating(false)}
          >
            Saved to the Book.
          </Notice>
        )}
      </CaseFrame>
    </section>
  );
}

type ConfirmCase = {
  id: string;
  label: string;
  title: string;
  detail: ReactNode;
  irreversible?: boolean;
  confirmLabel: string;
  evidence?: ReactNode;
  busy?: boolean;
  error?: unknown;
};

const confirmCases: ConfirmCase[] = [
  {
    id: "delete",
    label: "Permanent",
    title: "Delete this file?",
    detail: (
      <>
        <strong>Northwind invoice 0042.pdf</strong> will be permanently
        deleted.
      </>
    ),
    irreversible: true,
    confirmLabel: "Delete file",
  },
  {
    id: "disconnect",
    label: "With evidence",
    title: "Disconnect Example Agent?",
    detail:
      "This removes access to the Books below. Other connections keep their access.",
    confirmLabel: "Disconnect",
    evidence: (
      <ul className="gallery-evidence vult-type-ui">
        <li>Example Studio · Manage</li>
        <li>Example Holdings · View</li>
      </ul>
    ),
  },
  {
    id: "pending",
    label: "Pending",
    title: "Delete this file?",
    detail: (
      <>
        <strong>Northwind invoice 0042.pdf</strong> will be permanently
        deleted.
      </>
    ),
    irreversible: true,
    confirmLabel: "Delete file",
    busy: true,
  },
  {
    id: "failed",
    label: "Failed",
    title: "Delete this file?",
    detail: (
      <>
        <strong>Northwind invoice 0042.pdf</strong> will be permanently
        deleted.
      </>
    ),
    irreversible: true,
    confirmLabel: "Delete file",
    error: new TypeError("Failed to fetch"),
  },
];

function ConfirmSpecimen({ example }: { example: ConfirmCase }) {
  return (
    <div className="gallery-dialog vult-confirm" role="group" aria-label={example.title}>
      <div className="dialog-heading">
        <h2>{example.title}</h2>
      </div>
      <div className="vult-confirm-body">
        <ConfirmDetail irreversible={example.irreversible}>
          {example.detail}
        </ConfirmDetail>
        {example.evidence}
        <ErrorNotice error={example.error} intent="write" />
        <ConfirmFooter
          confirmLabel={example.confirmLabel}
          busy={example.busy}
          onCancel={() => undefined}
          onConfirm={() => undefined}
        />
      </div>
    </div>
  );
}

function ConfirmSection() {
  const [open, setOpen] = useState<ConfirmCase | null>(null);
  return (
    <section className="gallery-section" aria-label="Confirmations">
      <SectionHeader
        title="Confirmations"
        rules={[
          ["When", "Only for destructive or irreversible steps. Reversible changes save without asking."],
          ["Title", "The question, verb first: “Delete this file?”"],
          ["Detail", "What happens and what stays. Permanent steps end with “You can’t undo this.”"],
          ["Evidence", "What the decision affects, such as the Books that lose access."],
          ["Actions", "Cancel, then the verb and object. The label stays the same while pending."],
          ["Failure", "The error appears above the actions; confirming again retries the same request."],
        ]}
      />
      <div className="gallery-confirm-grid">
        {confirmCases.map((example) => (
          <CaseFrame
            key={example.id}
            id={`confirm-${example.id}`}
            title={example.label}
            chips={
              <Button color="tertiary" size="sm" onClick={() => setOpen(example)}>
                Open dialog
              </Button>
            }
          >
            <ConfirmSpecimen example={example} />
          </CaseFrame>
        ))}
      </div>
      <ConfirmAction
        open={!!open}
        title={open?.title || ""}
        detail={open?.detail}
        irreversible={open?.irreversible}
        confirmLabel={open?.confirmLabel || ""}
        error={open?.error}
        onConfirm={() => setOpen(null)}
        onCancel={() => setOpen(null)}
      >
        {open?.evidence}
      </ConfirmAction>
    </section>
  );
}

function Gallery() {
  const [theme, setTheme] = useState<"light" | "dark">(() =>
    window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light",
  );
  const [width, setWidth] = useState<"full" | "narrow">("full");
  const [section, setSection] = useState<Section>("all");
  const [severity, setSeverity] = useState<"all" | (typeof severities)[number]>(
    "all",
  );
  const [recovery, setRecovery] = useState<"all" | (typeof recoveries)[number]>(
    "all",
  );
  useEffect(() => {
    const root = document.documentElement;
    root.classList.toggle("dark-mode", theme === "dark");
    root.classList.toggle("light-mode", theme === "light");
    root.style.colorScheme = theme;
  }, [theme]);
  const show = (name: Exclude<Section, "all">) =>
    section === "all" || section === name;
  return (
    <main className="gallery">
      <header className="gallery-header">
        <div>
          <h1 className="vult-type-section-title">Interface states</h1>
          <p className="vult-type-ui gallery-muted">
            Loading, empty and failure states share one anatomy: icon, title,
            one line and the actions. Notices report what changed;
            confirmations stand before what can’t be taken back. Preview only;
            nothing here reads or changes a Book.
          </p>
        </div>
        <div className="gallery-controls">
          <Segmented
            label="Show"
            value={section}
            options={[
              { value: "all", label: "All" },
              { value: "lifecycle", label: "Lifecycle" },
              { value: "loading", label: "Loading" },
              { value: "empty", label: "Empty" },
              { value: "failure", label: "Failure" },
              { value: "notice", label: "Notices" },
              { value: "confirm", label: "Confirmations" },
            ]}
            onChange={setSection}
          />
          <Segmented
            label="Theme"
            value={theme}
            options={[
              { value: "light", label: "Light" },
              { value: "dark", label: "Dark" },
            ]}
            onChange={setTheme}
          />
          <Segmented
            label="Width"
            value={width}
            options={[
              { value: "full", label: "Full" },
              { value: "narrow", label: "Phone" },
            ]}
            onChange={setWidth}
          />
          {show("failure") && (
            <>
              <Segmented
                label="Failure severity"
                value={severity}
                options={[
                  { value: "all", label: "All" },
                  { value: "routine", label: "Routine" },
                  { value: "attention", label: "Attention" },
                  { value: "critical", label: "Critical" },
                ]}
                onChange={setSeverity}
              />
              <Segmented
                label="Failure recovery"
                value={recovery}
                options={[
                  { value: "all", label: "All" },
                  ...recoveries.map((value) => ({
                    value,
                    label: recoveryLabel[value],
                  })),
                ]}
                onChange={setRecovery}
              />
            </>
          )}
        </div>
      </header>
      <div className="gallery-body" data-width={width}>
        {show("lifecycle") && <LifecycleSection />}
        {show("loading") && <LoadingSection />}
        {show("empty") && <EmptySection />}
        {show("failure") && (
          <FailureSection severity={severity} recovery={recovery} />
        )}
        {show("notice") && <NoticeSection />}
        {show("confirm") && <ConfirmSection />}
      </div>
    </main>
  );
}

createRoot(document.getElementById("root")!).render(<Gallery />);

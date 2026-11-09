import { useEffect, useId, useRef, useState } from "react";
import { ChevronDown, PlugsConnected } from "../design-system/icons";
import { get, post, type Book, type BookList } from "../lib/api";
import { withRecentSignIn } from "../lib/recent-sign-in";
import { Button } from "./ui/button";
import { ErrorMessage, message } from "./form";
import { formatDate } from "../lib/date-format";
import { ConfirmAction } from "./ui/confirm-action";
import { toast } from "./ui/toaster";
import "./access-settings.css";
import { EmptyState } from "./ui/empty-state";
import { LoadingState } from "./ui/loading-state";
import { FailureState } from "./ui/failure-state";
import { errorCopy } from "../lib/error-copy";
import { readSession } from "../lib/session";

type BookPermission = { book_id: string; role?: string; rights: string[] };
type Connection = {
  id: string;
  name: string;
  connection?: boolean;
  created_at?: string;
  last_renewed_at?: string;
  revoked_at?: string;
  expires_at: string | null;
  book_grants?: BookPermission[];
  access?: string;
  last_recorded_at?: string;
};
type ConnectionList = { tokens: Connection[]; disconnected?: Connection[] };
type Access = "read" | "propose" | "write";

/** An agent counts as active when it recorded something in the last two weeks. */
const activeWindow = 14 * 24 * 60 * 60 * 1000;
type AgentStatus = "active" | "quiet" | "disconnected";
function agentStatus(items: Connection[]): AgentStatus {
  if (items.every((c) => c.revoked_at)) return "disconnected";
  const last = latest(items);
  return last && Date.now() - new Date(last).getTime() < activeWindow ? "active" : "quiet";
}
function latest(items: Connection[]) {
  return items.map((c) => c.last_recorded_at).filter((v): v is string => !!v).sort().at(-1);
}
const statusNames: Record<AgentStatus, string> = { active: "Active", quiet: "Idle", disconnected: "Disconnected" };
/** The three access levels, in the words the owner chooses between. */
const levels: { value: Access; label: string; detail: string }[] = [
  { value: "read", label: "Read", detail: "View only." },
  { value: "propose", label: "Propose", detail: "Drafts entries for your approval." },
  { value: "write", label: "Record", detail: "Posts entries directly." },
];
type Grant = { connection: Connection; bookID: string; access: Access };
const roleAccess: Record<string, Access> = { manage: "write", propose: "propose", view: "read" };
/** Each Book a connection reaches, at its level there. */
function grantsOf(connection: Connection): Grant[] {
  return (connection.book_grants ?? [])
    .filter((g) => g.role && g.role in roleAccess)
    .map((g) => ({ connection, bookID: g.book_id, access: roleAccess[g.role!] }));
}
function strongest(grants: Grant[]): Access {
  const order: Access[] = ["read", "propose", "write"];
  return grants.map((g) => g.access).reduce((a, b) => (order.indexOf(b) > order.indexOf(a) ? b : a), "read" as Access);
}
export function AccessSettings({
  book,
  onAvailabilityChange,
  active = true,
  probe = true,
}: {
  book: Book;
  onAvailabilityChange?: (available: boolean) => void;
  active?: boolean;
  probe?: boolean;
}) {
  const returnFocus = useRef<HTMLElement | null>(null);
  const [confirmation, setConfirmation] = useState<{
    connection: Connection;
    trigger: HTMLButtonElement;
  } | null>(null);
  const [disconnectError, setDisconnectError] = useState("");
  const [connectionStatus, setConnectionStatus] = useState<"loading" | "ready" | "error">("loading");
  const [connectionRefresh, setConnectionRefresh] = useState(0);
  const [bookNames, setBookNames] = useState<Record<string, string>>({});
  const [mode, setMode] = useState<"local" | "server">("server");
  const [disconnectedList, setDisconnectedList] = useState<Connection[]>([]);
  const [changing, setChanging] = useState("");
  const [enabled, setEnabled] = useState(false),
    [connections, setConnections] = useState<Connection[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    setConfirmation(null);
    setDisconnectError("");
  }, [book.id, active]);
  useEffect(() => {
    if (!probe) return;
    let current = true;
    void readSession()
      .then((session) => {
        // Local and server installations both list connections for the owner.
        if (current) {
          setMode(session?.mode === "local" ? "local" : "server");
          setEnabled(!!session);
          onAvailabilityChange?.(!!session);
        }
      })
      .catch((error) => {
        if (current) setError(message(error));
      });
    return () => {
      current = false;
    };
  }, [probe, onAvailabilityChange]);
  useEffect(() => {
    if (!active || !enabled) return;
    const controller = new AbortController();
    setConnectionStatus("loading");
    setError("");
    void Promise.all([
      get<ConnectionList>("access/tokens", controller.signal),
      get<BookList>("books", controller.signal).catch(() => ({
        books: [] as Book[],
      })),
    ])
      .then(([list, catalog]) => {
        if (!controller.signal.aborted) {
          setConnections(list.tokens);
          setDisconnectedList(list.disconnected ?? []);
          setConnectionStatus("ready");
          setBookNames(
            Object.fromEntries(
              (catalog.books || []).map((item) => [item.id, item.name]),
            ),
          );
        }
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          setError(message(error));
          setConnectionStatus("error");
        }
      });
    return () => controller.abort();
  }, [active, enabled, book.id, connectionRefresh]);
  async function disconnect(id: string) {
    if (busy) return;
    setBusy(true);
    setDisconnectError("");
    try {
      await post(`access/tokens/${id}/revoke`, {});
      const value = await get<ConnectionList>("access/tokens");
      setConnections(value.tokens);
      setDisconnectedList(value.disconnected ?? []);
      returnFocus.current = null;
      toast.success(`${confirmation?.connection.name || "The agent"} disconnected.`);
      setConfirmation(null);
    } catch (error) {
      setDisconnectError(message(error));
    } finally {
      setBusy(false);
    }
  }
  async function changeAccess(connection: Connection, access: Access) {
    if (changing || connection.access === access) return;
    setChanging(connection.id);
    setError("");
    try {
      await withRecentSignIn(() => post(`access/tokens/${connection.id}/access`, { access }));
      const value = await get<ConnectionList>("access/tokens");
      setConnections(value.tokens);
      setDisconnectedList(value.disconnected ?? []);
      toast.success(`${connection.name} can now ${levels.find((l) => l.value === access)!.label.toLowerCase()} in ${bookNames[connection.book_grants?.[0]?.book_id ?? ""] || "this Book"}.`);
    } catch (e) {
      setError(message(e));
    } finally {
      setChanging("");
    }
  }
  if (!enabled || !active) return null;
  const list = connections.filter(
    (connection) =>
      (connection.connection || connection.book_grants?.some(
        (grant) => grant.book_id === book.id && grant.role !== "none",
      )) &&
      !connection.revoked_at &&
      // A connection's grant is durable (expires_at null) until it is revoked.
      (connection.expires_at === null || new Date(connection.expires_at).getTime() > Date.now()),
  );
  const bookLabels = { ...bookNames, [book.id]: book.name };
  const group = (items: Connection[]) => {
    const groups = new Map<string, Connection[]>();
    // A display group never merges client identities, grants or revoke targets.
    for (const connection of items) {
      const key = `${connection.connection ? "app" : "legacy"}:${connection.name}`;
      groups.set(key, [...(groups.get(key) || []), connection]);
    }
    return [...groups];
  };
  const live = group(list);
  const gone = group(disconnectedList.filter((c) => !live.some(([, items]) => items.some((i) => i.name === c.name))));
  const activeCount = live.filter(([, items]) => agentStatus(items) === "active").length;
  return (
    <div className="book-settings-access">
      <div className="connection-settings">
        <section className="agents-panel" aria-labelledby="agents-title">
          <header className="agents-panel-header">
            <span className="agents-panel-icon" aria-hidden="true"><PlugsConnected /></span>
            <div className="agents-panel-titles">
              <h2 id="agents-title" className="vult-type-label">Agents</h2>
            </div>
            {live.length > 0 && <span className="agent-count vult-type-metadata">{activeCount} active</span>}
          </header>
          {live.length ? (
            <ul className="agent-list" aria-label="Connected agents">
              {live.map(([key, items]) => (
                <AgentRow
                  key={`${book.id}:${key}`}
                  connections={items}
                  bookNames={bookLabels}
                  busy={busy}
                  changing={changing}
                  onChangeAccess={changeAccess}
                  onDisconnect={(connection, trigger) => {
                    setDisconnectError("");
                    returnFocus.current = trigger;
                    setConfirmation({ connection, trigger });
                  }}
                />
              ))}
            </ul>
          ) : (
            <div className="agent-card agent-card-empty">
              {connectionStatus === "loading" ? (
                <LoadingState scope="region" label="Loading agents…" />
              ) : connectionStatus === "error" ? (
                <FailureState
                  scope="region"
                  error={errorCopy.unavailable}
                  title="Couldn’t load agents"
                  onRetry={() => setConnectionRefresh((value) => value + 1)}
                />
              ) : (
                <EmptyState
                  scope="region"
                  title="No agents connected"
                  detail={mode === "local" ? "Run vult mcp install, then approve it with your passkey." : "Add your Vult address to your agent as an MCP server."}
                />
              )}
            </div>
          )}
          {gone.length > 0 && <DisconnectedAgents groups={gone} bookNames={bookLabels} />}
        </section>
      </div>
      <ErrorMessage message={error} />
      <ConfirmAction
        open={!!confirmation}
        title={`Disconnect ${confirmation?.connection.name || "this agent"}?`}
        detail={
          confirmation?.connection.connection
            ? `It loses access to ${disconnectBooks(confirmation.connection, bookLabels)}.`
            : "Its token stops working."
        }
        confirmLabel="Disconnect"
        busy={busy}
        error={disconnectError}
        returnTo={returnFocus.current}
        onCancel={() => setConfirmation(null)}
        onConfirm={() => {
          if (confirmation) void disconnect(confirmation.connection.id);
        }}
      />
    </div>
  );
}

function StatusLine({ items }: { items: Connection[] }) {
  const status = agentStatus(items);
  const revoked = items.map((c) => c.revoked_at).filter((v): v is string => !!v).sort().at(-1);
  return (
    <span className="agent-row-status vult-type-metadata" data-status={status}>
      <span className="agent-row-dot" aria-hidden="true" />
      {statusNames[status]}
      {status === "disconnected" && revoked && <> · {formatDate(revoked.slice(0, 10))}</>}
    </span>
  );
}

function AgentRow({
  connections,
  bookNames,
  busy,
  changing,
  onChangeAccess,
  onDisconnect,
}: {
  connections: Connection[];
  bookNames: Record<string, string>;
  busy: boolean;
  changing: string;
  onChangeAccess: (connection: Connection, access: Access) => void;
  onDisconnect: (connection: Connection, trigger: HTMLButtonElement) => void;
}) {
  const detailsID = useId();
  const [expanded, setExpanded] = useState(false);
  const name = connections[0].name;
  const grants = connections.flatMap(grantsOf);
  const level = strongest(grants);
  const books = new Set(grants.map((g) => g.bookID)).size;
  return (
    <li className="agent-card" data-expanded={expanded || undefined}>
      <button
        type="button"
        className="agent-summary vult-focus-target"
        aria-expanded={expanded}
        aria-controls={detailsID}
        onClick={() => setExpanded((open) => !open)}
      >
        <ChevronDown className="agent-chevron" aria-hidden="true" />
        <span className="agent-identity">
          <span className="agent-name-line">
            <span className="agent-name vult-type-label">{name}</span>
          </span>
          <span className="agent-meta vult-type-metadata">
            <StatusLine items={connections} />
            <span className="agent-book-count">{books} {books === 1 ? "Book" : "Books"}</span>
          </span>
        </span>
        <span className="agent-level vult-type-metadata" data-level={level}>{levels.find((l) => l.value === level)!.label}</span>
      </button>
      <div id={detailsID} className="agent-details" hidden={!expanded}>
        {grants.map(({ connection, bookID, access }, index) => {
          const bookName = bookNames[bookID] || `Book ${bookID.slice(0, 8)}`;
          // A connection reaches one Book; only those can change level here.
          const changeable = !!connection.connection && grantsOf(connection).length === 1;
          const last = index === grants.length - 1 || grants[index + 1].connection.id !== connection.id;
          return (
            <div key={`${connection.id}:${bookID}`} className="agent-connection">
              <div className="agent-access">
                <div className="agent-access-text">
                  <span className="vult-type-label">{bookName}</span>
                  <span className="vult-type-metadata text-tertiary">{levels.find((l) => l.value === access)!.detail}</span>
                </div>
                {changeable ? <div className="agent-levels" role="radiogroup" aria-label={`${name} access in ${bookName}`}>
                  {levels.map((l) => (
                    <button
                      key={l.value}
                      type="button"
                      role="radio"
                      aria-checked={access === l.value}
                      className="agent-level-option vult-type-metadata vult-focus-target"
                      disabled={!!changing || busy}
                      onClick={() => onChangeAccess(connection, l.value)}
                    >
                      {l.label}
                    </button>
                  ))}
                </div> : <span className="agent-level vult-type-metadata" data-level={access}>{levels.find((l) => l.value === access)!.label}</span>}
              </div>
              {last && <div className="agent-connection-footer vult-type-metadata text-tertiary">
                <span>{connection.created_at ? <>Since {formatDate(connection.created_at.slice(0, 10))}</> : null}</span>
                <Button
                  color="tertiary"
                  size="sm"
                  aria-label={`Disconnect ${connection.name} connection ${connection.id.slice(0, 8)}`}
                  disabled={busy}
                  onClick={(event) => onDisconnect(connection, event.currentTarget)}
                >
                  Disconnect
                </Button>
              </div>}
            </div>
          );
        })}
      </div>
    </li>
  );
}

function DisconnectedAgents({ groups, bookNames }: { groups: [string, Connection[]][]; bookNames: Record<string, string> }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <div className="agent-history">
      <button type="button" className="agent-history-toggle vult-type-metadata vult-focus-target" aria-expanded={open} aria-controls={id} onClick={() => setOpen((v) => !v)}>
        {groups.length} disconnected
        <ChevronDown aria-hidden="true" />
      </button>
      <ul id={id} className="agent-list" hidden={!open} aria-label="Disconnected agents">
        {groups.map(([key, items]) => (
          <li key={key} className="agent-card" data-disconnected>
            <div className="agent-summary">
              <span className="agent-identity">
                <span className="agent-name-line">
                  <span className="agent-name vult-type-label">{items[0].name}</span>
                      </span>
                <StatusLine items={items} />
              </span>
              <span className="agent-books vult-type-metadata">
                {[...new Set(items.map((c) => bookNames[c.book_grants?.[0]?.book_id ?? ""]).filter(Boolean))].join(", ")}
              </span>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

function disconnectBooks(connection: Connection, bookNames: Record<string, string>) {
  const names = grantsOf(connection).map((g) => bookNames[g.bookID] || "this Book");
  return names.length ? names.join(", ") : "its Books";
}

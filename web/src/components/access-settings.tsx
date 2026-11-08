import { useEffect, useId, useRef, useState } from "react";
import { ChevronDown } from "../design-system/icons";
import { get, post, type Book, type BookList } from "../lib/api";
import { SettingsSection } from "./ui/settings-section";
import { Button } from "./ui/button";
import { ErrorMessage, message } from "./form";
import { formatReadableDateTime } from "../lib/date-format";
import { ConfirmAction } from "./ui/confirm-action";
import { Notice } from "./ui/notice";
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
};
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
  const [notice, setNotice] = useState("");
  const [connectionStatus, setConnectionStatus] = useState<"loading" | "ready" | "error">("loading");
  const [connectionRefresh, setConnectionRefresh] = useState(0);
  const [bookNames, setBookNames] = useState<Record<string, string>>({});
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
      get<{ tokens: Connection[] }>("access/tokens", controller.signal),
      get<BookList>("books", controller.signal).catch(() => ({
        books: [] as Book[],
      })),
    ])
      .then(([list, catalog]) => {
        if (!controller.signal.aborted) {
          setConnections(list.tokens);
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
      const value = await get<{ tokens: Connection[] }>("access/tokens");
      setConnections(value.tokens);
      returnFocus.current = null;
      setNotice(`${confirmation?.connection.name || "The app"} disconnected.`);
      setConfirmation(null);
    } catch (error) {
      setDisconnectError(message(error));
    } finally {
      setBusy(false);
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
  const groups = new Map<string, Connection[]>();
  const bookLabels = { ...bookNames, [book.id]: book.name };
  for (const connection of list) {
    // A display group never merges client identities, grants or revoke targets.
    const key = `${connection.connection ? "app" : "legacy"}:${connection.name}`;
    groups.set(key, [...(groups.get(key) || []), connection]);
  }
  return (
    <div className="book-settings-access">
      <div className="connection-settings">
        <SettingsSection
          title="Connected apps"
          description={<>Agents connected to your Books, and what each can reach. Connect an agent, or change its Books, from the terminal with <code className="vult-code">vult connection create</code>.</>}
        >
          {list.length ? (
            <ul
              className="connection-list"
              aria-label="Connected apps"
            >
              {[...groups].map(([key, items]) => (
                <ConnectionGroup
                  key={`${book.id}:${key}`}
                  connections={items}
                  bookID={book.id}
                  bookNames={bookLabels}
                  busy={busy}
                  onDisconnect={(connection, trigger) => {
                    setDisconnectError("");
                    returnFocus.current = trigger;
                    setConfirmation({ connection, trigger });
                  }}
                />
              ))}
            </ul>
          ) : connectionStatus !== "ready" ? (
            connectionStatus === "loading" ? (
              <LoadingState scope="region" label="Loading connected apps…" />
            ) : (
              <FailureState
                scope="region"
                error={errorCopy.unavailable}
                title="Couldn’t load connected apps"
                onRetry={() => setConnectionRefresh((value) => value + 1)}
              />
            )
          ) : (
            <EmptyState
              scope="region"
              title="No agents connected"
              detail="Connect one from the terminal: vult connection create."
            />
          )}
        </SettingsSection>
      </div>
      <ErrorMessage message={error} />
      {notice && <Notice>{notice}</Notice>}
      <ConfirmAction
        open={!!confirmation}
        title={`Disconnect ${confirmation?.connection.name || "this app"}${confirmation?.connection.connection ? " connection" : ""}?`}
        detail={
          confirmation?.connection.connection
            ? "This removes access to the Books below. Other connections keep their access."
            : "This revokes the token’s existing permissions. To use the app again, create a new connection in the terminal."
        }
        confirmLabel="Disconnect"
        busy={busy}
        error={disconnectError}
        returnTo={returnFocus.current}
        onCancel={() => setConfirmation(null)}
        onConfirm={() => {
          if (confirmation) void disconnect(confirmation.connection.id);
        }}
      >
        {confirmation?.connection.connection ? (
          <>
            <ConnectionBooks connection={confirmation.connection} bookNames={bookLabels} />
            <ConnectionDetails connection={confirmation.connection} />
          </>
        ) : null}
      </ConfirmAction>
    </div>
  );
}

function connectionAccess(connection: Connection, bookID: string) {
  if (!connection.connection) return "Legacy token";
  const count = connection.book_grants?.filter((item) => item.role === "view" || item.role === "manage").length || 0;
  return count ? `${count} ${count === 1 ? "Book" : "Books"}` : "No Book access";
}

function ConnectionGroup({
  connections,
  bookID,
  bookNames,
  busy,
  onDisconnect,
}: {
  connections: Connection[];
  bookID: string;
  bookNames: Record<string, string>;
  busy: boolean;
  onDisconnect: (connection: Connection, trigger: HTMLButtonElement) => void;
}) {
  const detailsID = useId();
  const [expanded, setExpanded] = useState(connections.length === 1);
  const name = connections[0].name;
  const accessedBooks = new Set(connections.flatMap((item) => (item.book_grants || []).filter((grant) => grant.role === "view" || grant.role === "manage").map((grant) => grant.book_id)));
  const accessSummary = connections[0].connection ? (accessedBooks.size ? `${accessedBooks.size} ${accessedBooks.size === 1 ? "Book" : "Books"}` : "No Book access") : "Legacy token";
  const count = connections.length;
  const authorization = connections[0].connection;
  const countLabel = authorization
    ? count === 1
      ? "connection"
      : "connections"
    : count === 1
      ? "token"
      : "tokens";
  return (
    <li className="connection-app-group">
      <div className="connection-group-summary">
        <div className="connection-app-identity">
          <p className="connection-group-name vult-type-label">{name}</p>
          <p className="connection-group-access vult-type-ui text-tertiary">
            {accessSummary}
          </p>
        </div>
        <Button
          color="tertiary"
          size="sm"
          className="connection-group-toggle"
          aria-label={`${count} ${countLabel} for ${name}`}
          aria-expanded={expanded}
          aria-controls={detailsID}
          disabled={busy}
          onClick={() => setExpanded((open) => !open)}
        >
          {count} {countLabel}
          <ChevronDown aria-hidden="true" />
        </Button>
      </div>
      <div
        id={detailsID}
        className="connection-group-details"
        hidden={!expanded}
      >
        <ul
          className="connection-list"
          aria-label={`${name} ${authorization ? "connections" : "tokens"}`}
        >
          {connections.map((connection) => (
            <li className="connection-app" key={connection.id}>
              {connection.connection ? <>
                {count > 1 && <ConnectionDetails connection={connection} />}
                {/* Read-only in the browser: which Books it reaches is changed in the terminal. */}
                <ConnectionBooks connection={connection} bookNames={bookNames} />
                <div className="connection-row-actions"><Button color="tertiary" size="sm" aria-label={`Disconnect ${connection.name} connection ${connection.id.slice(0, 8)}`} disabled={busy} onClick={(event) => onDisconnect(connection, event.currentTarget)}>Disconnect</Button></div>
              </> : <>
                <p className="vult-type-ui text-tertiary">{connectionAccess(connection, bookID)}</p>
                {connection.expires_at && <p className="vult-type-metadata text-tertiary">Expires {formatReadableDateTime(connection.expires_at)}</p>}
                <div className="connection-row-actions"><Button color="tertiary" size="sm" aria-label={`Disconnect ${connection.name} token ${connection.id.slice(0, 8)}`} disabled={busy} onClick={(event) => onDisconnect(connection, event.currentTarget)}>Disconnect</Button></div>
              </>}
            </li>
          ))}
        </ul>
      </div>
    </li>
  );
}

function ConnectionBooks({
  connection,
  bookNames,
}: {
  connection: Connection;
  bookNames: Record<string, string>;
}) {
  const grants =
    connection.book_grants?.filter(
      (grant) => grant.role === "manage" || grant.role === "view",
    ) || [];
  if (!grants.length) return <p className="vult-type-ui text-tertiary">No Book access</p>;
  return (
    <dl
      className="connection-book-permissions"
      aria-label="Books and permissions"
    >
      {grants.map((grant) => <div key={grant.book_id}>
          <dt className="vult-type-label">
            {bookNames[grant.book_id] || `Book ${grant.book_id.slice(0, 8)}`}
          </dt>
          <dd className="vult-type-ui text-tertiary">
            {grant.role === "manage" ? "Manage records" : "View only"}
          </dd>
        </div>)}
    </dl>
  );
}

function ConnectionDetails({
  connection,
}: {
  connection: Connection;
}) {
  if (!connection.created_at) return null;
  return (
    <div className="connection-details vult-type-metadata text-tertiary">
        <p>
          Connected{" "}
          <time
            dateTime={connection.created_at}
            title={formatReadableDateTime(connection.created_at, "full")}
          >
            {formatReadableDateTime(connection.created_at)}
          </time>
        </p>
    </div>
  );
}

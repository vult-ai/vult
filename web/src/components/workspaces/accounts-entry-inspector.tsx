import {
  createContext,
  Suspense,
  useCallback,
  useContext,
  useId,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { ContextualEntryPreview, EntryPreview } from "../book/entry-preview";
import "./accounts-entry-inspector.css";
import { LoadingState } from "../ui/loading-state";

type AccountEntryInspection = {
  id: string;
  pending: boolean;
  returnTo: HTMLElement | null;
  onClose: () => void;
  render: (id: string) => ReactNode;
};

const AccountEntryContext = createContext<AccountEntryInspection | null>(null);
export const AccountsEntryProvider = AccountEntryContext.Provider;
export const useAccountEntryInspection = () => useContext(AccountEntryContext);

/** Keep one reader mounted when its presentation changes between dock and modal. */
export function AccountsEntryInspector({ docked }: { docked: boolean }) {
  const entry = useAccountEntryInspection();
  const regionId = useId();
  const [host] = useState(() => document.createElement("div"));
  const attach = useCallback(
    (element: HTMLDivElement | null) => {
      if (element && host.parentElement !== element) element.appendChild(host);
    },
    [host],
  );
  if (!entry) return null;
  return (
    <>
      <div className="account-entry-inspector" data-open={docked && !!entry.id}>
        <EntryPreview
          id={docked ? entry.id : ""}
          regionId={regionId}
          pending={entry.pending}
          returnTo={entry.returnTo}
          onClose={entry.onClose}
        >
          {() => <div ref={attach} />}
        </EntryPreview>
      </div>
      <ContextualEntryPreview
        id={docked ? "" : entry.id}
        pending={entry.pending}
        returnTo={entry.returnTo}
        onClose={entry.onClose}
      >
        {() => <div ref={attach} />}
      </ContextualEntryPreview>
      {entry.id &&
        createPortal(
          <Suspense
            fallback={
              <LoadingState scope="inline" label="Loading entry…" />
            }
          >
            {entry.render(entry.id)}
          </Suspense>,
          host,
        )}
    </>
  );
}

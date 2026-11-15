import { useEffect, useRef } from "react";
import type { Book } from "../../lib/api";
import { bookPath } from "../../lib/book-routing";
import { dismissUpgradeNotice, readBackups } from "../../lib/backups";
import { toast } from "../ui/toaster";
import { restoreSection } from "./restore-settings";

/**
 * Once after an upgrade: one toast saying the Vult was backed up first,
 * with Restore… beside it, which opens Settings → Data at Restore. It stays until it's
 * closed or Restore… is used; only then is it gone for good.
 */
export function UpgradeNotice({ book, read = readBackups, dismiss = dismissUpgradeNotice, show = toast.lasting, open = (to: string) => window.location.assign(to) }: {
  book: Book;
  read?: typeof readBackups;
  dismiss?: typeof dismissUpgradeNotice;
  show?: typeof toast.lasting;
  open?: (to: string) => void;
}) {
  const shown = useRef(false);
  const where = useRef(book);
  where.current = book;
  useEffect(() => {
    const c = new AbortController();
    read(c.signal).then((b) => {
      const notice = b?.notice;
      if (!notice || shown.current) return;
      shown.current = true;
      // Closing it tells Vult it was seen. Restore… closes it too, and waits
      // for that (briefly) before leaving the page, which would cancel it.
      let seen: Promise<unknown> = Promise.resolve();
      const go = () => open(`${bookPath(where.current, "settings")}?tab=data#${restoreSection}`);
      show(`upgrade-${notice.backup_id}`, `Updated to ${notice.version}. Your Vult was backed up first.`,
        { label: "Restore…", onClick: () => void Promise.race([seen, new Promise((r) => setTimeout(r, 2000))]).then(go) },
        () => { seen = dismiss(notice.backup_id).catch(() => undefined); });
    }, () => undefined);
    return () => c.abort();
  }, [read, dismiss, show, open]);
  return null;
}

import { useState } from "react";
import type { Book } from "../../lib/api";
import { bookLogoURL } from "../../lib/book-logo";
import { profileInitials } from "../../lib/user-profile";
import "./book-mark.css";

/** A Book's mark: its logo or photo when one is set, otherwise its initials,
 * drawn by CSS so they never join the text of the name beside them. */
export function BookMark({ book, size = 20 }: { book: Pick<Book, "id" | "name" | "logo">; size?: 20 | 24 | 40 | 64 }) {
  const src = bookLogoURL(book);
  const [failed, setFailed] = useState<string>();
  return (
    <span className="book-mark" data-size={size} aria-hidden="true">
      {src && failed !== src ? <img src={src} alt="" onError={() => setFailed(src)} /> : <span className="book-mark-initials" data-initials={profileInitials(book.name)} />}
    </span>
  );
}

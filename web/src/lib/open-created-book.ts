import type { Book } from "./api";
import { bookPath, type RoutableBook } from "./book-routing";
import { fetchJSON } from "./errors";

type Address = NonNullable<RoutableBook["address"]> & { book_id: string };

/** Opens a Book that was just created, at the address Vult gave it. The Book
 * list carries addresses separately, so the new Book's is attached here. */
export async function openCreatedBook(created: Book) {
  try {
    const { addresses } = await fetchJSON<{ addresses?: Address[] }>("/api/v1/books", { cache: "no-store" });
    const address = addresses?.find((a) => a.book_id === created.id);
    if (address) {
      window.location.assign(bookPath({ id: created.id, name: created.name, address }));
      return;
    }
  } catch {
    /* Fall through to the Book list, where the new Book is listed. */
  }
  window.location.assign("/books");
}

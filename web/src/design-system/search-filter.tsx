import { XClose } from "./icons";
import { Button } from "./catalog/components/vult-ui/button";
import "./search-filter.css";

/** The Book search a collection is filtered by, shown as the filter it is
 * (docs/design/search.md). Searching happens in the finder (⌘K); this only
 * shows the search and removes it. */
export function SearchFilter({ value, onRemove }: { value: string; onRemove: () => void }) {
  return (
    <Button
      size="sm"
      color="secondary"
      iconTrailing={XClose}
      aria-label={`Remove search “${value}”`}
      className="vult-search-filter"
      onPress={onRemove}
    >
      “{value}”
    </Button>
  );
}

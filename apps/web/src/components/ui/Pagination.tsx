import { Button } from "./Button";

interface PaginationProps {
  /** Cursor for the previous page, or undefined at the first page. Matches
   * ListXRequest's page_token / ListXResponse's next_page_token contract —
   * there is no total count or page number, only "is there more." */
  hasPrevious: boolean;
  hasNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
}

export function Pagination({ hasPrevious, hasNext, onPrevious, onNext }: PaginationProps) {
  return (
    <nav className="flex items-center justify-end gap-2" aria-label="Pagination">
      <Button variant="secondary" size="sm" onClick={onPrevious} disabled={!hasPrevious}>
        Previous
      </Button>
      <Button variant="secondary" size="sm" onClick={onNext} disabled={!hasNext}>
        Next
      </Button>
    </nav>
  );
}

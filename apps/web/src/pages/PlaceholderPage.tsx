import { PageHeader } from "../components/shell/PageHeader";
import { EmptyState } from "../components/ui/StatePanel";

/** Accounts/Contacts/Leads/Opportunities screens are Phase 3 of task.md —
 * this keeps every sidebar link resolving to a real, honest page instead of
 * a 404 until that work lands. */
export default function PlaceholderPage({ title }: { title: string }) {
  return (
    <div>
      <PageHeader title={title} />
      <EmptyState
        title="Coming soon"
        description={`${title} workflows are on the roadmap (task.md Phase 3).`}
      />
    </div>
  );
}

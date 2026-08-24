import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { Select } from "./ui/Select";
import { renderQueryError, mutationErrorMessage } from "../lib/connectErrors";
import { useCrmClients } from "../lib/useCrmClients";
import { useAuth } from "../auth/AuthProvider";
import { useToast } from "./ui/Toast";

/** The record types tags may attach to — internal/domain/tags.go's
 * TaggableObjectTypes, kept in sync by hand since the allowlist is small and
 * rarely changes. */
export type TaggableObjectType = "account" | "contact" | "lead" | "opportunity" | "task";

/** The tags attached to one record, with an add/remove control for anyone
 * holding crm:tag:write. Read-only for a viewer who holds crm:tag:read
 * alone — the same badges render, just without the remove button or picker. */
export function TagsPanel({ objectType, objectId }: { objectType: TaggableObjectType; objectId: string }) {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [picked, setPicked] = useState("");

  const canRead = hasScope("crm:tag:read");
  const canWrite = hasScope("crm:tag:write");

  const recordKey = ["crm.listTagsForRecord", objectType, objectId] as const;
  const recordQuery = useQuery({
    queryKey: recordKey,
    queryFn: () => crm.listTagsForRecord({ objectType, objectId }),
    enabled: canRead && !!objectId,
  });
  const catalogQuery = useQuery({
    queryKey: ["crm.listTags"],
    queryFn: () => crm.listTags({}),
    enabled: canWrite,
  });

  const assign = useMutation({
    mutationFn: (tagId: string) => crm.assignTag({ tagId, objectType, objectId }),
    onSuccess: () => { setPicked(""); void queryClient.invalidateQueries({ queryKey: recordKey }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const remove = useMutation({
    mutationFn: (tagId: string) => crm.removeTag({ tagId, objectType, objectId }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: recordKey }),
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  if (!canRead) return null;
  if (recordQuery.error) {
    return (
      <section aria-labelledby="tags-heading" className="rounded-lg border border-slate-200 bg-white p-4">
        <h2 id="tags-heading" className="mb-2 text-sm font-semibold text-slate-900">Tags</h2>
        {renderQueryError(recordQuery.error, { onRetry: () => recordQuery.refetch() })}
      </section>
    );
  }

  const current = recordQuery.data?.tags ?? [];
  const currentIds = new Set(current.map((tag) => tag.id));
  const available = (catalogQuery.data?.tags ?? []).filter((tag) => !currentIds.has(tag.id));

  return (
    <section aria-labelledby="tags-heading" className="rounded-lg border border-slate-200 bg-white p-4">
      <h2 id="tags-heading" className="mb-2 text-sm font-semibold text-slate-900">Tags</h2>
      <div className="flex flex-wrap items-center gap-2">
        {current.length === 0 && <p className="text-sm text-slate-500">No tags yet.</p>}
        {current.map((tag) => (
          <Badge key={tag.id} tone="neutral">
            <span
              className="mr-1 inline-block h-2 w-2 rounded-full"
              style={{ backgroundColor: tag.color || "#94a3b8" }}
              aria-hidden="true"
            />
            {tag.name}
            {canWrite && (
              <button
                type="button"
                onClick={() => remove.mutate(tag.id)}
                disabled={remove.isPending}
                aria-label={`Remove tag ${tag.name}`}
                className="ml-1 text-slate-500 hover:text-danger-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600"
              >
                ✕
              </button>
            )}
          </Badge>
        ))}
      </div>
      {canWrite && available.length > 0 && (
        <div className="mt-3 flex items-end gap-2">
          <div className="w-48">
            <Select
              label="Add tag"
              value={picked}
              onChange={(event) => setPicked(event.target.value)}
              placeholder="Choose a tag…"
              options={available.map((tag) => ({ value: tag.id, label: tag.name }))}
            />
          </div>
          <Button
            size="sm"
            variant="secondary"
            disabled={!picked}
            loading={assign.isPending}
            onClick={() => picked && assign.mutate(picked)}
          >
            Add
          </Button>
        </div>
      )}
    </section>
  );
}

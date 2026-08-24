import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import type { Account } from "@crm/contracts";
import { Dialog } from "../../components/ui/Dialog";
import { Button } from "../../components/ui/Button";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useToast } from "../../components/ui/Toast";

/** The Account fields a merge can resolve per field — matches the server's
 * own allowlist (internal/domain/merge_accounts.go's mergeableAccountFields).
 * A field not listed here (id, timestamps, custom fields, the dedup flag
 * itself) is never offered, because the server would reject it.
 *
 * serverField is the exact key the server's allowlist expects in
 * field_resolutions — snake_case, because that map is a flat string map
 * with no proto-generated field-name translation, not the TS client's own
 * camelCase Account property name. Sending the camelCase key (e.g.
 * "ownerId") is a request the server correctly rejects as unresolvable. */
const MERGE_FIELDS: { key: keyof Account & string; serverField: string; label: string }[] = [
  { key: "name", serverField: "name", label: "Name" },
  { key: "industry", serverField: "industry", label: "Industry" },
  { key: "tier", serverField: "tier", label: "Tier" },
  { key: "ownerId", serverField: "owner_id", label: "Owner" },
  { key: "website", serverField: "website", label: "Website" },
  { key: "annualRevenue", serverField: "annual_revenue", label: "Annual revenue" },
  { key: "parentId", serverField: "parent_id", label: "Parent account" },
];

function displayValue(account: Account, key: string): string {
  const value = (account as unknown as Record<string, unknown>)[key];
  if (value === "" || value === undefined || value === null || value === 0) return "—";
  return String(value);
}

/** Reviews and merges the account currently on screen (the survivor, kept)
 * with a flagged duplicate. Field-by-field: each field defaults to the
 * survivor's own value and can be switched to the duplicate's. There is no
 * undo — see internal/domain/merge_accounts.go — so this is presented as a
 * deliberate, one-way action, not a quiet background sync. */
export function MergeAccountsDialog({
  open,
  onClose,
  survivorId,
  duplicateId,
  onMerged,
}: {
  open: boolean;
  onClose: () => void;
  survivorId: string;
  duplicateId: string;
  onMerged: () => void;
}) {
  const { crm } = useCrmClients();
  const toast = useToast();
  const [resolutions, setResolutions] = useState<Record<string, "survivor" | "duplicate">>({});

  const survivorQuery = useQuery({
    queryKey: ["crm.getAccount", survivorId],
    queryFn: () => crm.getAccount({ id: survivorId }),
    enabled: open,
  });
  const duplicateQuery = useQuery({
    queryKey: ["crm.getAccount", duplicateId],
    queryFn: () => crm.getAccount({ id: duplicateId }),
    enabled: open,
  });

  useEffect(() => {
    if (open) setResolutions({});
  }, [open, survivorId, duplicateId]);

  const merge = useMutation({
    mutationFn: () => {
      const fieldResolutions: Record<string, string> = {};
      for (const { key, serverField } of MERGE_FIELDS) {
        if (resolutions[key] === "duplicate") fieldResolutions[serverField] = "duplicate";
      }
      return crm.mergeAccounts({ survivorId, duplicateId, fieldResolutions });
    },
    onSuccess: () => {
      toast.show({ tone: "success", message: "Accounts merged." });
      onMerged();
    },
    onError: (err) => toast.show({ tone: "danger", message: mutationErrorMessage(err) }),
  });

  const survivor = survivorQuery.data?.account;
  const duplicate = duplicateQuery.data?.account;
  const loading = survivorQuery.isPending || duplicateQuery.isPending;
  const loadError = survivorQuery.error ?? duplicateQuery.error;

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Review and merge duplicate account"
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={merge.isPending}>
            Cancel
          </Button>
          <Button
            variant="danger"
            onClick={() => merge.mutate()}
            loading={merge.isPending}
            disabled={!survivor || !duplicate}
          >
            Merge — cannot be undone
          </Button>
        </>
      }
    >
      {loading && <SkeletonRows rows={4} />}
      {loadError && renderQueryError(loadError, {})}
      {survivor && duplicate && (
        <div className="flex flex-col gap-4">
          <p className="text-sm text-slate-600">
            <strong>{survivor.name}</strong> will be kept. <strong>{duplicate.name}</strong> will be deleted
            after its contacts, opportunities, tasks, notes, activities, tags, and child accounts move to{" "}
            {survivor.name}. This cannot be undone.
          </p>
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-slate-200 text-left text-xs font-semibold uppercase tracking-wide text-slate-500">
                <th className="py-2 pr-3">Field</th>
                <th className="py-2 pr-3">Keep {survivor.name}</th>
                <th className="py-2">Use {duplicate.name}</th>
              </tr>
            </thead>
            <tbody>
              {MERGE_FIELDS.map(({ key, label }) => {
                const survivorValue = displayValue(survivor, key);
                const duplicateValue = displayValue(duplicate, key);
                const differ = survivorValue !== duplicateValue;
                const chosen = resolutions[key] ?? "survivor";
                return (
                  <tr key={key} className={`border-b border-slate-100 ${differ ? "" : "text-slate-400"}`}>
                    <td className="py-2 pr-3 font-medium text-slate-700">{label}</td>
                    <td className="py-2 pr-3">
                      <label className="flex items-center gap-2">
                        <input
                          type="radio"
                          name={`merge-field-${key}`}
                          checked={chosen === "survivor"}
                          disabled={!differ}
                          onChange={() => setResolutions((prev) => ({ ...prev, [key]: "survivor" }))}
                        />
                        {survivorValue}
                      </label>
                    </td>
                    <td className="py-2">
                      {differ ? (
                        <label className="flex items-center gap-2">
                          <input
                            type="radio"
                            name={`merge-field-${key}`}
                            checked={chosen === "duplicate"}
                            onChange={() => setResolutions((prev) => ({ ...prev, [key]: "duplicate" }))}
                          />
                          {duplicateValue}
                        </label>
                      ) : (
                        <span>{duplicateValue}</span>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Dialog>
  );
}

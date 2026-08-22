import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Button } from "./ui/Button";
import { Dialog } from "./ui/Dialog";
import { Select } from "./ui/Select";
import { SkeletonRows } from "./ui/Skeleton";
import { renderQueryError, mutationErrorMessage } from "../lib/connectErrors";
import { useCrmClients } from "../lib/useCrmClients";
import { useAuth } from "../auth/AuthProvider";
import { useToast } from "./ui/Toast";

// "note" is deliberately absent now that notes are a first-class entity
// (task.md §5.1). Offering both would give the same word two meanings on one
// screen: an immutable log line here, and an editable note in NotesPanel.
// Activities already recorded with kind "note" still render — this only stops
// new ones being created down the wrong path.
const activityKinds = [
  { value: "call", label: "Call" },
  { value: "email", label: "Email" },
  { value: "meeting", label: "Meeting" },
];

export function ActivityTimeline({ subjectType, subjectId }: { subjectType: "account" | "contact" | "lead" | "opportunity"; subjectId: string }) {
  const { crm } = useCrmClients();
  const { hasScope, session } = useAuth();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [logging, setLogging] = useState(false);
  const [kind, setKind] = useState("call");
  const [notes, setNotes] = useState("");
  const canWrite = hasScope("crm:activity:write");
  const key = ["crm.listActivitiesForSubject", subjectType, subjectId] as const;
  const query = useQuery({ queryKey: key, queryFn: () => crm.listActivitiesForSubject({ subjectType, subjectId }), enabled: !!subjectId });
  const create = useMutation({
    mutationFn: () => crm.createActivity({ activity: { kind, notes: notes.trim(), subjectType, subjectId, actorId: session?.claims.sub ?? "", occurredAt: timestampFromDate(new Date()) } }),
    onSuccess: () => { setLogging(false); setNotes(""); queryClient.invalidateQueries({ queryKey: key }); toast.show({ tone: "success", message: "Activity recorded." }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  return <section className="mt-4 max-w-2xl rounded-lg border border-slate-200 bg-white p-4">
    <div className="mb-3 flex items-center justify-between gap-3"><h2 className="text-sm font-semibold text-slate-900">Activity timeline</h2>{canWrite && <Button size="sm" onClick={() => setLogging(true)}>Log activity</Button>}</div>
    {query.isPending && <SkeletonRows rows={3} />}
    {query.error && renderQueryError(query.error, { onRetry: () => query.refetch(), forbiddenDescription: "You don't have permission to view this timeline." })}
    {query.data?.activities.length === 0 && <p className="text-sm text-slate-500">No activity recorded.</p>}
    {query.data?.activities.length ? <ol className="space-y-3">{query.data.activities.map((activity) => <li key={activity.id} className="border-l-2 border-slate-200 pl-3 text-sm"><p className="font-medium capitalize text-slate-800">{activity.kind}</p>{activity.notes && <p className="mt-0.5 whitespace-pre-wrap text-slate-600">{activity.notes}</p>}<p className="mt-1 text-xs text-slate-500">{activity.occurredAt ? timestampDate(activity.occurredAt).toLocaleString() : "Recorded"}</p></li>)}</ol> : null}
    <Dialog open={logging} onClose={() => setLogging(false)} title="Log activity" footer={<><Button variant="secondary" onClick={() => setLogging(false)} disabled={create.isPending}>Cancel</Button><Button onClick={() => create.mutate()} loading={create.isPending}>Save activity</Button></>}><div className="flex flex-col gap-4"><Select label="Type" value={kind} onChange={(event) => setKind(event.target.value)} options={activityKinds} /><label className="flex flex-col gap-1 text-sm font-medium text-slate-700">Notes<textarea value={notes} onChange={(event) => setNotes(event.target.value)} maxLength={4000} rows={4} className="rounded-md border border-slate-300 px-3 py-2 font-normal text-slate-900 outline-none focus:border-brand-500 focus:ring-2 focus:ring-brand-100" placeholder="What happened?" /></label></div></Dialog>
  </section>;
}

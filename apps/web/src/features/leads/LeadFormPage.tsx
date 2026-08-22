import { useEffect } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { PageHeader } from "../../components/shell/PageHeader";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Button } from "../../components/ui/Button";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { UnsavedChangesPrompt } from "../../components/ui/UnsavedChangesPrompt";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useUnsavedChangesGuard } from "../../lib/useUnsavedChangesGuard";
import { useToast } from "../../components/ui/Toast";

const schema = z.object({
  company: z.string().trim().min(1, "Company is required").max(200, "Company must be 200 characters or fewer"),
  contactEmail: z.string().trim().max(320, "Email is too long").optional().default("").refine(
    (value) => value === "" || z.string().email().safeParse(value).success,
    "Enter a valid email address",
  ),
  source: z.string().max(100, "Source must be 100 characters or fewer").optional().default(""),
  ownerId: z.string().trim().min(1, "Owner is required"),
  score: z.coerce.number().int("Score must be a whole number").min(0, "Score cannot be negative").default(0),
  status: z.enum(["new", "working", "qualified", "disqualified"]),
});

type FormValues = z.infer<typeof schema>;

function statusOptions(current: FormValues["status"]) {
  const allowed: Record<FormValues["status"], { value: FormValues["status"]; label: string }[]> = {
    new: [{ value: "new", label: "New" }, { value: "working", label: "Working" }],
    working: [{ value: "working", label: "Working" }, { value: "qualified", label: "Qualified" }],
    qualified: [{ value: "qualified", label: "Qualified" }, { value: "disqualified", label: "Disqualified" }],
    disqualified: [{ value: "disqualified", label: "Disqualified" }],
  };
  return allowed[current];
}

export default function LeadFormPage({ mode }: { mode: "create" | "edit" }) {
  const { id = "" } = useParams<{ id: string }>();
  const { crm } = useCrmClients();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();

  const leadQuery = useQuery({
    queryKey: ["crm.getLead", id],
    queryFn: () => crm.getLead({ id }),
    enabled: mode === "edit",
  });
  const existing = leadQuery.data?.lead;

  const {
    register,
    handleSubmit,
    reset,
    watch,
    formState: { errors, isDirty, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { company: "", contactEmail: "", source: "", ownerId: "", score: 0, status: "new" },
  });

  useEffect(() => {
    if (!existing) return;
    reset({
      company: existing.company,
      contactEmail: existing.contactEmail,
      source: existing.source,
      ownerId: existing.ownerId,
      score: Number(existing.score),
      status: existing.status === "converted" ? "qualified" : (existing.status as FormValues["status"]),
    });
  }, [existing, reset]);

  const currentStatus = watch("status");
  const terminal = existing?.status === "converted" || existing?.status === "disqualified";
  const { blocker, disarm } = useUnsavedChangesGuard(isDirty && !isSubmitting);

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      const lead = { ...values, score: BigInt(values.score) };
      if (mode === "create") return (await crm.createLead({ lead })).lead;
      return (
        await crm.updateLead({
          lead: {
            ...lead,
            id,
            status: existing?.status === "converted" ? "converted" : values.status,
            duplicateOfContactId: existing?.duplicateOfContactId ?? "",
            convertedContactId: existing?.convertedContactId ?? "",
            convertedAccountId: existing?.convertedAccountId ?? "",
            convertedOpportunityId: existing?.convertedOpportunityId ?? "",
            // UpdateX replaces the custom field document rather than merging
            // it, so anything this form omits is deleted. This form has no
            // custom-field inputs, so it round-trips what it read.
            customFields: existing?.customFields,
            updatedAt: existing?.updatedAt,
          },
        })
      ).lead;
    },
    onSuccess: (saved) => {
      disarm();
      queryClient.invalidateQueries({ queryKey: ["crm.listLeads"] });
      toast.show({ tone: saved?.duplicateOfContactId ? "warning" : "success", message: saved?.duplicateOfContactId ? "Lead saved — its email matches an existing contact." : "Lead saved." });
      navigate(saved ? `/leads/${saved.id}` : "/leads");
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  if (mode === "edit" && leadQuery.isPending) {
    return <div><PageHeader title="Edit lead" breadcrumbs={[{ label: "Leads", to: "/leads" }]} /><SkeletonRows rows={5} /></div>;
  }
  if (mode === "edit" && leadQuery.error) {
    return <div><PageHeader title="Edit lead" breadcrumbs={[{ label: "Leads", to: "/leads" }]} />{renderQueryError(leadQuery.error, { onRetry: () => leadQuery.refetch() })}</div>;
  }

  return (
    <div>
      <PageHeader
        title={mode === "create" ? "New lead" : `Edit ${existing?.company ?? "lead"}`}
        breadcrumbs={[{ label: "Leads", to: "/leads" }, ...(mode === "edit" && existing ? [{ label: existing.company, to: `/leads/${id}` }] : [])]}
      />
      {terminal && <p className="mb-4 max-w-xl rounded-md border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-600">This lead is {existing?.status}, a terminal state, and cannot be edited.</p>}
      <form onSubmit={handleSubmit((values) => mutation.mutate(values))} className="flex max-w-xl flex-col gap-4" noValidate>
        <Input label="Company" {...register("company")} error={errors.company?.message} required disabled={terminal} />
        <Input label="Contact email" type="email" {...register("contactEmail")} error={errors.contactEmail?.message} hint="Required before a qualified lead can be converted." disabled={terminal} />
        <Input label="Source" placeholder="e.g. Webinar, referral" {...register("source")} error={errors.source?.message} disabled={terminal} />
        <Input label="Owner ID" {...register("ownerId")} error={errors.ownerId?.message} required disabled={terminal} />
        <Input label="Score" type="number" min={0} step={1} {...register("score")} error={errors.score?.message} disabled={terminal} />
        <Select label="Status" options={statusOptions(currentStatus)} {...register("status")} error={errors.status?.message} disabled={terminal} hint="Leads move forward through the lifecycle; conversion is performed from the lead page." />
        <div className="mt-2 flex gap-2">
          {!terminal && <Button type="submit" loading={mutation.isPending}>{mode === "create" ? "Create lead" : "Save changes"}</Button>}
          <Button type="button" variant="secondary" onClick={() => navigate(mode === "edit" ? `/leads/${id}` : "/leads")}>Cancel</Button>
        </div>
      </form>
      <UnsavedChangesPrompt blocker={blocker} />
    </div>
  );
}

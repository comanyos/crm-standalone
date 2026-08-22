import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { PageHeader } from "../../components/shell/PageHeader";
import { AccountPicker } from "../../components/AccountPicker";
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
  name: z.string().trim().min(1, "Name is required").max(200, "Name must be 200 characters or fewer"),
  accountId: z.string().min(1, "Select an account"),
  amount: z.coerce.number().min(0, "Amount cannot be negative").default(0),
  probability: z.coerce.number().min(0, "Probability cannot be negative").max(100, "Probability cannot exceed 100").default(0),
  pipelineId: z.string().default(""),
  stageId: z.string().default(""),
  closeReason: z.string().max(500, "Close reason must be 500 characters or fewer").default(""),
});

type FormValues = z.infer<typeof schema>;

export default function OpportunityFormPage({ mode }: { mode: "create" | "edit" }) {
  const { id = "" } = useParams<{ id: string }>();
  const { crm } = useCrmClients();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [accountLabel, setAccountLabel] = useState("");

  const opportunityQuery = useQuery({ queryKey: ["crm.getOpportunity", id], queryFn: () => crm.getOpportunity({ id }), enabled: mode === "edit" });
  const existing = opportunityQuery.data?.opportunity;
  const accountQuery = useQuery({ queryKey: ["crm.getAccount", existing?.accountId], queryFn: () => crm.getAccount({ id: existing!.accountId }), enabled: !!existing?.accountId });

  const { register, control, handleSubmit, reset, watch, formState: { errors, isDirty, isSubmitting } } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", accountId: "", amount: 0, probability: 0, pipelineId: "", stageId: "", closeReason: "" },
  });

  useEffect(() => {
    if (!existing) return;
    reset({ name: existing.name, accountId: existing.accountId, amount: existing.amount, probability: existing.probability, pipelineId: existing.pipelineId, stageId: existing.stageId, closeReason: existing.closeReason });
  }, [existing, reset]);
  useEffect(() => {
    if (accountQuery.data?.account) setAccountLabel(accountQuery.data.account.name);
  }, [accountQuery.data]);

  const pipelineId = watch("pipelineId");
  const stageId = watch("stageId");
  const pipelinesQuery = useQuery({ queryKey: ["crm.listPipelines"], queryFn: () => crm.listPipelines({}) });
  const stagesQuery = useQuery({ queryKey: ["crm.listPipelineStages", pipelineId], queryFn: () => crm.listPipelineStages({ pipelineId }), enabled: !!pipelineId });
  const stages = stagesQuery.data?.stages ?? [];
  const currentStage = stages.find((stage) => stage.id === stageId);
  const existingStage = stages.find((stage) => stage.id === existing?.stageId);
  const hasOpenStageAfterCurrent = stages.some((stage) => stage.terminalType === "open" && stage.position > (existingStage?.position ?? -1) && !stage.archivedAt);
  const stageOptions = mode === "create" ? [] : stages.filter((stage) => stage.id === existing?.stageId || (!stage.archivedAt && (stage.terminalType !== "open" ? !hasOpenStageAfterCurrent : stage.position === (existingStage?.position ?? -1) + 1))).map((stage) => ({ value: stage.id, label: stage.name }));
  const closesNow = mode === "edit" && currentStage?.terminalType !== "open" && stageId !== existing?.stageId;
  const { blocker, disarm } = useUnsavedChangesGuard(isDirty && !isSubmitting);

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      if (mode === "create") return (await crm.createOpportunity({ opportunity: values })).opportunity;
      return (await crm.updateOpportunity({ opportunity: {
        ...values,
        id,
        // The backend stamps this on the closing write and rejects a client-supplied date.
        closeDate: closesNow ? undefined : existing?.closeDate,
        // UpdateOpportunity replaces the custom field document rather than
        // merging it, so anything this form does not send is deleted. This
        // form has no custom-field inputs, so it must round-trip what it read:
        // without it, saving an edit wiped every optional custom value on the
        // record, and failed outright for a tenant with a required one.
        customFields: existing?.customFields,
        updatedAt: existing?.updatedAt,
      } })).opportunity;
    },
    onSuccess: (saved) => {
      disarm();
      queryClient.invalidateQueries({ queryKey: ["crm.listOpportunities"] });
      queryClient.invalidateQueries({ queryKey: ["crm.getOpportunity", id] });
      toast.show({ tone: "success", message: `${saved?.name ?? "Opportunity"} saved.` });
      navigate(saved ? `/opportunities/${saved.id}` : "/opportunities");
    },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  if (mode === "edit" && opportunityQuery.isPending) return <div><PageHeader title="Edit opportunity" breadcrumbs={[{ label: "Opportunities", to: "/opportunities" }]} /><SkeletonRows rows={5} /></div>;
  if (mode === "edit" && opportunityQuery.error) return <div><PageHeader title="Edit opportunity" breadcrumbs={[{ label: "Opportunities", to: "/opportunities" }]} />{renderQueryError(opportunityQuery.error, { onRetry: () => opportunityQuery.refetch() })}</div>;

  const terminal = mode === "edit" && existingStage?.terminalType !== "open";
  return (
    <div>
      <PageHeader title={mode === "create" ? "New opportunity" : `Edit ${existing?.name ?? "opportunity"}`} breadcrumbs={[{ label: "Opportunities", to: "/opportunities" }, ...(mode === "edit" && existing ? [{ label: existing.name, to: `/opportunities/${id}` }] : [])]} />
      <form onSubmit={handleSubmit((values) => mutation.mutate(values))} className="flex max-w-xl flex-col gap-4" noValidate>
        <Input label="Opportunity name" {...register("name")} error={errors.name?.message} required />
        <Controller control={control} name="accountId" render={({ field }) => <AccountPicker label="Account" value={field.value} valueLabel={accountLabel} onChange={(accountId, accountName) => { field.onChange(accountId); setAccountLabel(accountName); }} error={errors.accountId?.message} disabled={terminal} />} />
        <div className="grid grid-cols-2 gap-4">
          <Input label="Amount" type="number" min={0} step="any" {...register("amount")} error={errors.amount?.message} disabled={terminal} />
          <Input label="Probability (%)" type="number" min={0} max={100} step="any" {...register("probability")} error={errors.probability?.message} disabled={terminal} />
        </div>
        {mode === "create" && <Select label="Pipeline" options={[{ value: "", label: "Use organization default" }, ...((pipelinesQuery.data?.pipelines ?? []).filter((pipeline) => !pipeline.archivedAt).map((pipeline) => ({ value: pipeline.id, label: pipeline.name })))]} {...register("pipelineId")} hint="New opportunities begin at the first open stage." />}
        {terminal ? <Input label="Stage" value={existingStage?.name ?? existing?.stage ?? ""} readOnly /> : mode === "edit" ? <Select label="Stage" options={stageOptions} {...register("stageId")} error={errors.stageId?.message} hint="Stages advance according to this pipeline's configured order." /> : null}
        {(closesNow || terminal) && <Input label="Close reason" {...register("closeReason")} error={errors.closeReason?.message} required={closesNow && !!currentStage?.requiresCloseReason} readOnly={terminal} hint={closesNow && currentStage?.requiresCloseReason ? "Required by this pipeline stage." : undefined} />}
        <div className="mt-2 flex gap-2"><Button type="submit" loading={mutation.isPending} disabled={terminal}>{mode === "create" ? "Create opportunity" : "Save changes"}</Button><Button type="button" variant="secondary" onClick={() => navigate(mode === "edit" ? `/opportunities/${id}` : "/opportunities")}>Cancel</Button></div>
      </form>
      <UnsavedChangesPrompt blocker={blocker} />
    </div>
  );
}

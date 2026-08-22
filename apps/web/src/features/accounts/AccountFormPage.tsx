import { useEffect } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { PageHeader } from "../../components/shell/PageHeader";
import { Input } from "../../components/ui/Input";
import { Button } from "../../components/ui/Button";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { UnsavedChangesPrompt } from "../../components/ui/UnsavedChangesPrompt";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useUnsavedChangesGuard } from "../../lib/useUnsavedChangesGuard";
import { useToast } from "../../components/ui/Toast";

// Mirrors internal/domain/types.go's field limits — the backend remains the
// authority (this is UX, not the security boundary), but matching limits
// means a validation error never reaches the network round trip.
const schema = z.object({
  name: z.string().trim().min(1, "Name is required").max(200, "Name must be 200 characters or fewer"),
  industry: z.string().max(100, "Industry must be 100 characters or fewer").optional().default(""),
  tier: z.string().max(50, "Tier must be 50 characters or fewer").optional().default(""),
  ownerId: z.string().trim().min(1, "Owner is required"),
  website: z
    .string()
    .max(2048, "Website must be 2048 characters or fewer")
    .optional()
    .default("")
    .refine((v) => v === "" || /^https?:\/\/.+/.test(v), "Website must start with http:// or https://"),
  annualRevenue: z.coerce.number().min(0, "Annual revenue cannot be negative").optional().default(0),
});

type FormValues = z.infer<typeof schema>;

export default function AccountFormPage({ mode }: { mode: "create" | "edit" }) {
  const { id = "" } = useParams<{ id: string }>();
  const { crm } = useCrmClients();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();

  const accountQuery = useQuery({
    queryKey: ["crm.getAccount", id],
    queryFn: () => crm.getAccount({ id }),
    enabled: mode === "edit",
  });
  const existing = accountQuery.data?.account;

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isDirty, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { name: "", industry: "", tier: "", ownerId: "", website: "", annualRevenue: 0 },
  });

  useEffect(() => {
    if (existing) {
      reset({
        name: existing.name,
        industry: existing.industry,
        tier: existing.tier,
        ownerId: existing.ownerId,
        website: existing.website,
        annualRevenue: existing.annualRevenue,
      });
    }
  }, [existing, reset]);

  const { blocker, disarm } = useUnsavedChangesGuard(isDirty && !isSubmitting);

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      if (mode === "create") {
        return (await crm.createAccount({ account: { ...values } })).account;
      }
      // UpdateAccount replaces the full record — parent_id and
      // duplicate_of_account_id aren't editable in this form, so they're
      // carried over from the loaded record rather than silently cleared.
      // duplicate_of_account_id is server-recomputed on every write anyway
      // (internal/domain/account_hierarchy.go), but sending it back is
      // harmless and keeps this call symmetric with parent_id's handling.
      return (
        await crm.updateAccount({
          account: {
            ...values,
            id,
            parentId: existing?.parentId ?? "",
            duplicateOfAccountId: existing?.duplicateOfAccountId ?? "",
            updatedAt: existing?.updatedAt,
          },
        })
      ).account;
    },
    onSuccess: (saved) => {
      // The form is still dirty here; without this the navigation below
      // would prompt to discard changes that were just saved successfully.
      disarm();
      // The form is still dirty here; without this the navigation below
      // would prompt to discard changes that were just saved successfully.
      queryClient.invalidateQueries({ queryKey: ["crm.listAccounts"] });
      toast.show({ tone: "success", message: `${saved?.name ?? "Account"} saved.` });
      navigate(saved ? `/accounts/${saved.id}` : "/accounts");
    },
    onError: (err) => {
      toast.show({ tone: "danger", message: mutationErrorMessage(err) });
    },
  });

  if (mode === "edit" && accountQuery.isPending) {
    return (
      <div>
        <PageHeader title="Edit account" breadcrumbs={[{ label: "Accounts", to: "/accounts" }]} />
        <SkeletonRows rows={5} />
      </div>
    );
  }

  if (mode === "edit" && accountQuery.error) {
    return (
      <div>
        <PageHeader title="Edit account" breadcrumbs={[{ label: "Accounts", to: "/accounts" }]} />
        {renderQueryError(accountQuery.error, { onRetry: () => accountQuery.refetch() })}
      </div>
    );
  }

  return (
    <div>
      <PageHeader
        title={mode === "create" ? "New account" : `Edit ${existing?.name ?? ""}`}
        breadcrumbs={[
          { label: "Accounts", to: "/accounts" },
          ...(mode === "edit" && existing ? [{ label: existing.name, to: `/accounts/${id}` }] : []),
        ]}
      />

      <form
        onSubmit={handleSubmit((values) => mutation.mutate(values))}
        className="flex max-w-xl flex-col gap-4"
        noValidate
      >
        <Input label="Name" {...register("name")} error={errors.name?.message} required />
        <Input label="Industry" {...register("industry")} error={errors.industry?.message} />
        <Input
          label="Tier"
          {...register("tier")}
          error={errors.tier?.message}
          hint="Free text, e.g. gold, silver."
        />
        <Input label="Owner ID" {...register("ownerId")} error={errors.ownerId?.message} />
        <Input
          label="Website"
          type="url"
          placeholder="https://example.com"
          {...register("website")}
          error={errors.website?.message}
        />
        <Input
          label="Annual revenue"
          type="number"
          min={0}
          step="any"
          {...register("annualRevenue")}
          error={errors.annualRevenue?.message}
        />

        <div className="mt-2 flex gap-2">
          <Button type="submit" loading={mutation.isPending}>
            {mode === "create" ? "Create account" : "Save changes"}
          </Button>
          <Button
            type="button"
            variant="secondary"
            onClick={() => navigate(mode === "edit" ? `/accounts/${id}` : "/accounts")}
          >
            Cancel
          </Button>
        </div>
      </form>

      <UnsavedChangesPrompt blocker={blocker} />
    </div>
  );
}

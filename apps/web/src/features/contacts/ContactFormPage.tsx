import { useEffect, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm, Controller } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { PageHeader } from "../../components/shell/PageHeader";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Button } from "../../components/ui/Button";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { UnsavedChangesPrompt } from "../../components/ui/UnsavedChangesPrompt";
import { AccountPicker } from "../../components/AccountPicker";
import { renderQueryError, mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";
import { useUnsavedChangesGuard } from "../../lib/useUnsavedChangesGuard";
import { useToast } from "../../components/ui/Toast";

const ROLE_OPTIONS = [
  { value: "billing", label: "Billing" },
  { value: "technical", label: "Technical" },
  { value: "decision_maker", label: "Decision maker" },
  { value: "other", label: "Other" },
];

// Mirrors internal/domain/types.go's field limits and email shape check —
// see AccountFormPage's schema comment for why this duplicates, not
// replaces, backend validation.
const schema = z.object({
  email: z
    .string()
    .trim()
    .min(1, "Email is required")
    .max(320, "Email is too long")
    .email("Enter a valid email address"),
  accountId: z.string().min(1, "Select an account"),
  firstName: z.string().max(200).optional().default(""),
  lastName: z.string().max(200).optional().default(""),
  phone: z.string().max(32, "Phone must be 32 characters or fewer").optional().default(""),
  title: z.string().max(200).optional().default(""),
  role: z.string().optional().default(""),
});

type FormValues = z.infer<typeof schema>;

export default function ContactFormPage({ mode }: { mode: "create" | "edit" }) {
  const { id = "" } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const { crm } = useCrmClients();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const toast = useToast();
  const [accountLabel, setAccountLabel] = useState("");

  const contactQuery = useQuery({
    queryKey: ["crm.getContact", id],
    queryFn: () => crm.getContact({ id }),
    enabled: mode === "edit",
  });
  const existing = contactQuery.data?.contact;

  const prefilledAccountId = mode === "create" ? (searchParams.get("accountId") ?? "") : "";
  const prefilledAccountName = mode === "create" ? (searchParams.get("accountName") ?? "") : "";

  const {
    register,
    control,
    handleSubmit,
    reset,
    formState: { errors, isDirty, isSubmitting },
  } = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: {
      email: "",
      accountId: prefilledAccountId,
      firstName: "",
      lastName: "",
      phone: "",
      title: "",
      role: "",
    },
  });

  useEffect(() => {
    if (prefilledAccountName) setAccountLabel(prefilledAccountName);
  }, [prefilledAccountName]);

  const existingAccountQuery = useQuery({
    queryKey: ["crm.getAccount", existing?.accountId],
    queryFn: () => crm.getAccount({ id: existing!.accountId }),
    enabled: !!existing?.accountId,
  });

  useEffect(() => {
    if (existing) {
      reset({
        email: existing.email,
        accountId: existing.accountId,
        firstName: existing.firstName,
        lastName: existing.lastName,
        phone: existing.phone,
        title: existing.title,
        role: existing.role,
      });
      if (existingAccountQuery.data?.account) setAccountLabel(existingAccountQuery.data.account.name);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [existing, existingAccountQuery.data]);

  const { blocker, disarm } = useUnsavedChangesGuard(isDirty && !isSubmitting);

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      if (mode === "create") {
        return (await crm.createContact({ contact: { ...values } })).contact;
      }
      return (
        await crm.updateContact({
          contact: {
            ...values,
            id,
            // UpdateX replaces the custom field document rather than merging
            // it, so anything this form omits is deleted. This form has no
            // custom-field inputs, so it round-trips what it read.
            customFields: existing?.customFields,
            updatedAt: existing?.updatedAt,
          },
        })
      ).contact;
    },
    onSuccess: (saved) => {
      // The form is still dirty here; without this the navigation below
      // would prompt to discard changes that were just saved successfully.
      disarm();
      queryClient.invalidateQueries({ queryKey: ["crm.listContacts"] });
      if (saved?.duplicateOfContactId) {
        toast.show({
          tone: "warning",
          message:
            "Saved — this email matches another contact. Review both before assuming they're the same person.",
          duration: 8000,
        });
      } else {
        toast.show({ tone: "success", message: "Contact saved." });
      }
      navigate(saved ? `/contacts/${saved.id}` : "/contacts");
    },
    onError: (err) => {
      toast.show({ tone: "danger", message: mutationErrorMessage(err) });
    },
  });

  if (mode === "edit" && contactQuery.isPending) {
    return (
      <div>
        <PageHeader title="Edit contact" breadcrumbs={[{ label: "Contacts", to: "/contacts" }]} />
        <SkeletonRows rows={5} />
      </div>
    );
  }

  if (mode === "edit" && contactQuery.error) {
    return (
      <div>
        <PageHeader title="Edit contact" breadcrumbs={[{ label: "Contacts", to: "/contacts" }]} />
        {renderQueryError(contactQuery.error, { onRetry: () => contactQuery.refetch() })}
      </div>
    );
  }

  const existingDisplayName =
    existing &&
    (existing.firstName || existing.lastName
      ? `${existing.firstName} ${existing.lastName}`.trim()
      : existing.email);

  return (
    <div>
      <PageHeader
        title={mode === "create" ? "New contact" : `Edit ${existingDisplayName}`}
        breadcrumbs={[
          { label: "Contacts", to: "/contacts" },
          ...(mode === "edit" && existing ? [{ label: existingDisplayName!, to: `/contacts/${id}` }] : []),
        ]}
      />

      <form
        onSubmit={handleSubmit((values) => mutation.mutate(values))}
        className="flex max-w-xl flex-col gap-4"
        noValidate
      >
        <Input label="Email" type="email" {...register("email")} error={errors.email?.message} required />

        <Controller
          control={control}
          name="accountId"
          render={({ field }) => (
            <AccountPicker
              label="Account"
              value={field.value}
              valueLabel={accountLabel}
              onChange={(accountId, accountName) => {
                field.onChange(accountId);
                setAccountLabel(accountName);
              }}
              error={errors.accountId?.message}
            />
          )}
        />

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <Input label="First name" {...register("firstName")} error={errors.firstName?.message} />
          <Input label="Last name" {...register("lastName")} error={errors.lastName?.message} />
        </div>
        <Input label="Phone" {...register("phone")} error={errors.phone?.message} />
        <Input label="Title" {...register("title")} error={errors.title?.message} />
        <Select
          label="Role"
          placeholder="Not set"
          options={ROLE_OPTIONS}
          {...register("role")}
          error={errors.role?.message}
        />

        <div className="mt-2 flex gap-2">
          <Button type="submit" loading={mutation.isPending}>
            {mode === "create" ? "Create contact" : "Save changes"}
          </Button>
          <Button
            type="button"
            variant="secondary"
            onClick={() => navigate(mode === "edit" ? `/contacts/${id}` : "/contacts")}
          >
            Cancel
          </Button>
        </div>
      </form>

      <UnsavedChangesPrompt blocker={blocker} />
    </div>
  );
}

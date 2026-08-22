import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Dialog } from "../../components/ui/Dialog";
import { Input } from "../../components/ui/Input";
import { Select } from "../../components/ui/Select";
import { Table } from "../../components/ui/Table";
import { EmptyState } from "../../components/ui/StatePanel";
import { SkeletonRows } from "../../components/ui/Skeleton";
import { useAuth } from "../../auth/AuthProvider";
import { useCrmClients } from "../../lib/useCrmClients";
import { mutationErrorMessage, renderQueryError } from "../../lib/connectErrors";
import { useToast } from "../../components/ui/Toast";

const objectTypes = ["account", "contact", "lead", "opportunity", "task", "note", "activity"];
const fieldTypes = [
  { value: "text", label: "Text" }, { value: "long_text", label: "Long text" },
  { value: "number", label: "Number" }, { value: "currency", label: "Currency" },
  { value: "percent", label: "Percent" }, { value: "boolean", label: "Boolean" },
  { value: "date", label: "Date" }, { value: "datetime", label: "Date and time" },
  { value: "picklist", label: "Picklist" }, { value: "multi_picklist", label: "Multi-picklist" },
  { value: "lookup", label: "Lookup" },
];

type Draft = { id?: string; label: string; fieldKey: string; dataType: string; required: boolean; options: string; lookupObjectType: string; scale: string; visibleToRoles: string; editableToRoles: string };
const blankDraft = (): Draft => ({ label: "", fieldKey: "", dataType: "text", required: false, options: "", lookupObjectType: "account", scale: "2", visibleToRoles: "", editableToRoles: "" });

function configFor(draft: Draft) {
  return {
    options: (draft.dataType === "picklist" || draft.dataType === "multi_picklist")
      ? draft.options.split("\n").map((value) => value.trim()).filter(Boolean) : [],
    lookupObjectType: draft.dataType === "lookup" ? draft.lookupObjectType : "",
    scale: draft.dataType === "currency" || draft.dataType === "percent" ? Number(draft.scale) : 0,
    visibleToRoles: draft.visibleToRoles.split(",").map((value) => value.trim()).filter(Boolean),
    editableToRoles: draft.editableToRoles.split(",").map((value) => value.trim()).filter(Boolean),
  };
}

export default function CustomFieldsPage() {
  const { crm } = useCrmClients();
  const { hasScope } = useAuth();
  const toast = useToast();
  const queryClient = useQueryClient();
  const [objectType, setObjectType] = useState("account");
  const [dialogOpen, setDialogOpen] = useState(false);
  const [draft, setDraft] = useState<Draft>(blankDraft());
  const canWrite = hasScope("crm:custom_field:write");
  const canRead = hasScope("crm:custom_field:read");
  const fieldsQuery = useQuery({ queryKey: ["crm.listCustomFieldDefinitions", objectType], queryFn: () => crm.listCustomFieldDefinitions({ objectType, includeArchived: true }), enabled: canRead });
  const fields = fieldsQuery.data?.definitions ?? [];
  const closeDialog = () => { setDialogOpen(false); setDraft(blankDraft()); };
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["crm.listCustomFieldDefinitions", objectType] });
  const save = useMutation({
    mutationFn: () => draft.id
      ? crm.updateCustomFieldDefinition({ definition: { id: draft.id, label: draft.label.trim(), required: draft.required, config: configFor(draft) } }).then(() => undefined)
      : crm.createCustomFieldDefinition({ definition: { objectType, fieldKey: draft.fieldKey.trim(), label: draft.label.trim(), dataType: draft.dataType, required: draft.required, config: configFor(draft) } }).then(() => undefined),
    onSuccess: () => { closeDialog(); void refresh(); toast.show({ tone: "success", message: "Custom field saved." }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });
  const archive = useMutation({
    mutationFn: (id: string) => crm.archiveCustomFieldDefinition({ id }),
    onSuccess: () => { void refresh(); toast.show({ tone: "success", message: "Custom field archived. Existing values are retained." }); },
    onError: (error) => toast.show({ tone: "danger", message: mutationErrorMessage(error) }),
  });

  if (!canRead) return <div><PageHeader title="Custom fields" /><EmptyState title="Administrator access required" description="Custom-field definitions are managed by CRM administrators." /></div>;
  const startEdit = (field: (typeof fields)[number]) => {
    setDraft({ id: field.id, label: field.label, fieldKey: field.fieldKey, dataType: field.dataType, required: field.required, options: field.config?.options.join("\n") ?? "", lookupObjectType: field.config?.lookupObjectType || "account", scale: String(field.config?.scale ?? 2), visibleToRoles: field.config?.visibleToRoles.join(", ") ?? "", editableToRoles: field.config?.editableToRoles.join(", ") ?? "" });
    setDialogOpen(true);
  };
  const needsOptions = draft.dataType === "picklist" || draft.dataType === "multi_picklist";
  const needsScale = draft.dataType === "currency" || draft.dataType === "percent";
  return <div>
    <PageHeader title="Custom fields" actions={canWrite && <Button onClick={() => { setDraft(blankDraft()); setDialogOpen(true); }}>New field</Button>} />
    <p className="mb-5 max-w-3xl text-sm text-slate-600">Add tenant-owned fields to shipped CRM objects. Field keys and types are permanent once values exist; archive a field to retire it without losing historical data.</p>
    <div className="mb-5 max-w-xs"><Select label="Object" value={objectType} onChange={(event) => setObjectType(event.target.value)} options={objectTypes.map((value) => ({ value, label: value[0].toUpperCase() + value.slice(1) }))} /></div>
    {fieldsQuery.isPending && <SkeletonRows rows={4} />}
    {fieldsQuery.error && renderQueryError(fieldsQuery.error, { onRetry: () => fieldsQuery.refetch(), forbiddenDescription: "You don't have permission to view custom fields." })}
    {fieldsQuery.data && fields.length === 0 && <EmptyState title={`No ${objectType} custom fields`} description="Add a field to extend this object without a deploy." action={canWrite && <Button onClick={() => { setDraft(blankDraft()); setDialogOpen(true); }}>New field</Button>} />}
    {fields.length > 0 && <Table caption={`${objectType} custom fields`} rows={fields} getRowKey={(field) => field.id} columns={[
      { key: "label", header: "Field", render: (field) => <><div className="font-medium">{field.label}</div><div className="text-xs text-slate-500">{field.fieldKey}</div></> },
      { key: "type", header: "Type", render: (field) => field.dataType },
      { key: "required", header: "Required", render: (field) => field.required ? "Yes" : "No" },
      { key: "status", header: "Status", render: (field) => field.archivedAt ? "Archived" : "Active" },
      { key: "actions", header: "", render: (field) => canWrite && <div className="flex justify-end gap-2"><Button size="sm" variant="secondary" onClick={() => startEdit(field)}>Edit</Button>{!field.archivedAt && <Button size="sm" variant="secondary" loading={archive.isPending} onClick={() => { if (window.confirm(`Archive ${field.label}? Existing values will remain readable.`)) archive.mutate(field.id); }}>Archive</Button>}</div> },
    ]} />}
    <Dialog open={dialogOpen} onClose={closeDialog} title={draft.id ? "Edit custom field" : "New custom field"} footer={<><Button variant="secondary" onClick={closeDialog} disabled={save.isPending}>Cancel</Button><Button onClick={() => save.mutate()} loading={save.isPending} disabled={!draft.label.trim() || (!draft.id && !draft.fieldKey.trim()) || (needsOptions && !draft.options.trim())}>Save field</Button></>}>
      <div className="grid gap-3"><Input label="Label" value={draft.label} onChange={(event) => setDraft({ ...draft, label: event.target.value })} required />
        {!draft.id && <Input label="Field key" value={draft.fieldKey} onChange={(event) => setDraft({ ...draft, fieldKey: event.target.value.toLowerCase().replace(/[^a-z0-9_]/g, "") })} hint="Lowercase letters, digits, and underscores. This cannot be changed later." required />}
        {!draft.id && <Select label="Type" value={draft.dataType} onChange={(event) => setDraft({ ...draft, dataType: event.target.value })} options={fieldTypes} />}
        {needsOptions && <label className="flex flex-col gap-1 text-sm font-medium text-slate-700">Options (one per line)<textarea className="min-h-28 rounded-md border border-slate-300 px-3 py-2 text-sm" value={draft.options} onChange={(event) => setDraft({ ...draft, options: event.target.value })} /></label>}
        {draft.dataType === "lookup" && <Select label="Related object" value={draft.lookupObjectType} onChange={(event) => setDraft({ ...draft, lookupObjectType: event.target.value })} options={objectTypes.slice(0, 4).map((value) => ({ value, label: value[0].toUpperCase() + value.slice(1) }))} />}
        {needsScale && <Input label="Decimal places" type="number" min={0} max={12} value={draft.scale} onChange={(event) => setDraft({ ...draft, scale: event.target.value })} />}
        <label className="flex items-center gap-2 text-sm text-slate-700"><input type="checkbox" checked={draft.required} onChange={(event) => setDraft({ ...draft, required: event.target.checked })} /> Required for new and updated records</label>
        <Input label="Visible to roles" value={draft.visibleToRoles} onChange={(event) => setDraft({ ...draft, visibleToRoles: event.target.value })} hint="Comma-separated role keys. Leave empty for all roles." />
        <Input label="Editable by roles" value={draft.editableToRoles} onChange={(event) => setDraft({ ...draft, editableToRoles: event.target.value })} hint="Comma-separated role keys. Leave empty for all roles; CRM administrators always retain access." />
      </div>
    </Dialog>
  </div>;
}

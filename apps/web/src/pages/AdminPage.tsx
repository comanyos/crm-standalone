import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuth } from "../auth/AuthProvider";
import {
  createAdminMember,
  createAdminOrganization,
  listAdminMembers,
  listAdminOrganizations,
  listAdminUsers,
  listOrganizationJoinRequests,
  reviewOrganizationJoinRequest,
  rotateOrganizationJoinCode,
  setOrganizationJoinCodeEnabled,
  updateAdminMember,
  updateAdminOrganization,
  type AdminMember,
  type AdminOrganization,
  type AdminUser,
  type CrmRole,
  type OrganizationJoinRequest,
  type OrganizationPlan,
} from "../auth/admin-api";
import { PageHeader } from "../components/shell/PageHeader";
import { Badge, type BadgeTone } from "../components/ui/Badge";
import { Button } from "../components/ui/Button";
import { Dialog } from "../components/ui/Dialog";
import { Input } from "../components/ui/Input";
import { Select } from "../components/ui/Select";
import { EmptyState, ErrorState } from "../components/ui/StatePanel";
import { Table, type Column } from "../components/ui/Table";
import { useToast } from "../components/ui/Toast";
import { useAppEnv } from "../lib/AppEnvContext";

const ROLE_OPTIONS = [
  { value: "crm_admin", label: "Administrator" },
  { value: "crm_manager", label: "Manager" },
  { value: "crm_member", label: "Member" },
  { value: "crm_viewer", label: "Viewer" },
];

const PLAN_OPTIONS = [
  { value: "free", label: "Free" },
  { value: "pro", label: "Pro" },
  { value: "enterprise", label: "Enterprise" },
];

const ADMIN_AREAS = [
  { to: "/settings/integrations", title: "Connected accounts", description: "Provider connections and sync." },
  { to: "/settings/lead-capture", title: "Lead capture", description: "Inbound lead endpoints." },
  { to: "/settings/automation", title: "Automation", description: "Organization workflow rules." },
  { to: "/settings/pipelines", title: "Pipelines", description: "Sales stages and process." },
  { to: "/settings/custom-fields", title: "Data model", description: "Custom fields and CRM structure." },
  { to: "/settings/message-templates", title: "Message templates", description: "Reusable organization messaging." },
];

const emptyOrganization = {
  name: "",
  plan: "pro" as OrganizationPlan,
  adminEmail: "",
  adminFirstName: "",
  adminLastName: "",
};

const emptyMember = {
  email: "",
  firstName: "",
  lastName: "",
  role: "crm_member" as CrmRole,
};

interface EditableMember {
  userId: string;
  organizationId: string;
  email: string;
  firstName: string;
  lastName: string;
  role: CrmRole;
  active: boolean;
  superAdmin: boolean;
}

function displayRole(role: string): string {
  return ROLE_OPTIONS.find((option) => option.value === role)?.label ?? (role || "No access");
}

function displayPlan(plan: string): string {
  return PLAN_OPTIONS.find((option) => option.value === plan)?.label ?? "Unassigned";
}

function planTone(plan: string): BadgeTone {
  if (plan === "enterprise") return "warning";
  if (plan === "pro") return "brand";
  return "neutral";
}

function messageFrom(error: unknown): string {
  return error instanceof Error ? error.message : "The administration change could not be saved.";
}

function Metric({ label, value, detail }: { label: string; value: number; detail: string }) {
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
      <dt className="text-[10px] font-semibold uppercase tracking-[0.14em] text-slate-500">{label}</dt>
      <dd className="mt-2 text-3xl font-semibold tracking-tight text-slate-950">{value}</dd>
      <p className="mt-1 text-xs text-slate-500">{detail}</p>
    </div>
  );
}

export default function AdminPage() {
  const { hasRole, session } = useAuth();
  const { crmApiUrl } = useAppEnv();
  const toast = useToast();
  const queryClient = useQueryClient();
  const superAdmin = hasRole("platform_admin");
  const workspaceAdmin = hasRole("crm_admin");
  const allowed = superAdmin || workspaceAdmin;

  const [selectedOrganizationID, setSelectedOrganizationID] = useState(session?.organization?.id ?? "");
  const [organizationDialog, setOrganizationDialog] = useState(false);
  const [organizationForm, setOrganizationForm] = useState(emptyOrganization);
  const [editOrganizationDialog, setEditOrganizationDialog] = useState(false);
  const [organizationEdit, setOrganizationEdit] = useState({
    name: "",
    plan: "free" as OrganizationPlan,
    active: true,
  });
  const [memberDialog, setMemberDialog] = useState(false);
  const [memberForm, setMemberForm] = useState(emptyMember);
  const [editingMember, setEditingMember] = useState<EditableMember | null>(null);
  const [removingMember, setRemovingMember] = useState<EditableMember | null>(null);
  const [memberEdit, setMemberEdit] = useState({ role: "crm_member" as CrmRole, active: true });
  const [reviewingRequest, setReviewingRequest] = useState<OrganizationJoinRequest | null>(null);
  const [approvalRole, setApprovalRole] = useState("crm_member" as CrmRole);
  const [revealedJoinCode, setRevealedJoinCode] = useState<{ organizationId: string; code: string } | null>(null);

  const organizations = useQuery({
    queryKey: ["admin", "organizations"],
    queryFn: () => listAdminOrganizations(crmApiUrl),
    enabled: allowed,
  });

  const registeredUsers = useQuery({
    queryKey: ["admin", "users"],
    queryFn: () => listAdminUsers(crmApiUrl),
    enabled: superAdmin,
  });

  // The API excludes the protected platform principal and its internal
  // session anchor. Keep the same boundary here so stale cached responses can
  // never count them as a customer, seat, organization, or plan.
  const organizationRows = useMemo(
    () =>
      (organizations.data ?? []).filter(
        (organization) => !superAdmin || organization.id !== session?.organization?.id,
      ),
    [organizations.data, session?.organization?.id, superAdmin],
  );
  const userRows = useMemo(
    () =>
      (registeredUsers.data ?? []).filter(
        (user) => !user.superAdmin && user.userId !== session?.user.id,
      ),
    [registeredUsers.data, session?.user.id],
  );

  useEffect(() => {
    if (organizationRows.length === 0) return;
    if (!organizationRows.some((organization) => organization.id === selectedOrganizationID)) {
      setSelectedOrganizationID(organizationRows[0].id);
    }
  }, [organizationRows, selectedOrganizationID]);

  const selectedOrganization = useMemo(
    () => organizationRows.find((organization) => organization.id === selectedOrganizationID),
    [organizationRows, selectedOrganizationID],
  );

  const members = useQuery({
    queryKey: ["admin", "members", selectedOrganizationID],
    queryFn: () => listAdminMembers(crmApiUrl, selectedOrganizationID),
    enabled: workspaceAdmin && !superAdmin && !!selectedOrganizationID,
  });

  const joinRequests = useQuery({
    queryKey: ["admin", "join-requests", selectedOrganizationID],
    queryFn: () => listOrganizationJoinRequests(crmApiUrl, selectedOrganizationID),
    enabled: allowed && !!selectedOrganizationID,
  });

  const refreshOrganization = async (organizationID: string) => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["admin", "organizations"] }),
      queryClient.invalidateQueries({ queryKey: ["admin", "members", organizationID] }),
      queryClient.invalidateQueries({ queryKey: ["admin", "users"] }),
      queryClient.invalidateQueries({ queryKey: ["admin", "join-requests", organizationID] }),
    ]);
  };

  const createOrganizationMutation = useMutation({
    mutationFn: () => createAdminOrganization(crmApiUrl, organizationForm),
    onSuccess: async (organization) => {
      setSelectedOrganizationID(organization.id);
      setOrganizationDialog(false);
      setOrganizationForm(emptyOrganization);
      await refreshOrganization(organization.id);
      toast.show({ tone: "success", message: `${organization.name} is ready.` });
    },
    onError: (error) => toast.show({ tone: "danger", message: messageFrom(error), duration: 0 }),
  });

  const updateOrganizationMutation = useMutation({
    mutationFn: () =>
      updateAdminOrganization(crmApiUrl, selectedOrganizationID, {
        name: organizationEdit.name,
        ...(superAdmin ? { plan: organizationEdit.plan, active: organizationEdit.active } : {}),
      }),
    onSuccess: async (organization) => {
      setEditOrganizationDialog(false);
      await refreshOrganization(organization.id);
      toast.show({ tone: "success", message: "Organization settings saved." });
    },
    onError: (error) => toast.show({ tone: "danger", message: messageFrom(error), duration: 0 }),
  });

  const createMemberMutation = useMutation({
    mutationFn: () => createAdminMember(crmApiUrl, { organizationId: selectedOrganizationID, ...memberForm }),
    onSuccess: async () => {
      setMemberDialog(false);
      setMemberForm(emptyMember);
      await refreshOrganization(selectedOrganizationID);
      toast.show({ tone: "success", message: "User access granted. They can now log in with email OTP." });
    },
    onError: (error) => toast.show({ tone: "danger", message: messageFrom(error), duration: 0 }),
  });

  const updateMemberMutation = useMutation({
    mutationFn: () => {
      if (!editingMember) throw new Error("Select a user first.");
      if (editingMember.superAdmin) throw new Error("The Savvy super administrator is permanently protected.");
      return updateAdminMember(crmApiUrl, editingMember.organizationId, editingMember.userId, memberEdit);
    },
    onSuccess: async () => {
      const organizationID = editingMember?.organizationId ?? selectedOrganizationID;
      setEditingMember(null);
      await refreshOrganization(organizationID);
      toast.show({ tone: "success", message: "User access updated." });
    },
    onError: (error) => toast.show({ tone: "danger", message: messageFrom(error), duration: 0 }),
  });

  const removeMemberMutation = useMutation({
    mutationFn: (member: EditableMember) => {
      if (member.superAdmin) throw new Error("The Savvy super administrator is permanently protected.");
      if (member.userId === session?.user.id) throw new Error("You cannot remove your own active access.");
      return updateAdminMember(crmApiUrl, member.organizationId, member.userId, { active: false });
    },
    onSuccess: async (_, member) => {
      setRemovingMember(null);
      await refreshOrganization(member.organizationId);
      toast.show({
        tone: "success",
        message: "User access removed. CRM data and audit history were retained.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: messageFrom(error), duration: 0 }),
  });

  const rotateJoinCodeMutation = useMutation({
    mutationFn: () => rotateOrganizationJoinCode(crmApiUrl, selectedOrganizationID),
    onSuccess: async (joinCode) => {
      if (joinCode.code) setRevealedJoinCode({ organizationId: selectedOrganizationID, code: joinCode.code });
      await refreshOrganization(selectedOrganizationID);
      toast.show({ tone: "success", message: "Organization code generated. Copy it now; only the hint is stored." });
    },
    onError: (error) => toast.show({ tone: "danger", message: messageFrom(error), duration: 0 }),
  });

  const toggleJoinCodeMutation = useMutation({
    mutationFn: (enabled: boolean) =>
      setOrganizationJoinCodeEnabled(crmApiUrl, selectedOrganizationID, enabled),
    onSuccess: async (joinCode) => {
      await refreshOrganization(selectedOrganizationID);
      toast.show({
        tone: "success",
        message: joinCode.enabled ? "Organization code enabled." : "Organization code paused.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: messageFrom(error), duration: 0 }),
  });

  const reviewJoinRequestMutation = useMutation({
    mutationFn: ({ request, decision, role }: {
      request: OrganizationJoinRequest;
      decision: "approve" | "reject";
      role?: CrmRole;
    }) => reviewOrganizationJoinRequest(crmApiUrl, request.organizationId, request.id, decision, role),
    onSuccess: async (request) => {
      setReviewingRequest(null);
      await refreshOrganization(request.organizationId);
      toast.show({
        tone: "success",
        message: request.status === "approved" ? "Access approved. The user can now log in." : "Access request rejected.",
      });
    },
    onError: (error) => toast.show({ tone: "danger", message: messageFrom(error), duration: 0 }),
  });

  const openOrganizationSettings = (organization: AdminOrganization) => {
    setSelectedOrganizationID(organization.id);
    setOrganizationEdit({ name: organization.name, plan: organization.plan, active: organization.active });
    setEditOrganizationDialog(true);
  };

  const openAddUser = (organizationID?: string) => {
    const targetOrganizationID =
      organizationID ??
      organizationRows.find((organization) => organization.active)?.id ??
      organizationRows[0]?.id ??
      "";
    if (targetOrganizationID) setSelectedOrganizationID(targetOrganizationID);
    setMemberDialog(true);
  };

  const openMemberSettings = (member: AdminMember | AdminUser) => {
    if (member.superAdmin || !member.organizationId || !member.role) return;
    setEditingMember({
      userId: member.userId,
      organizationId: member.organizationId,
      email: member.email,
      firstName: member.firstName,
      lastName: member.lastName,
      role: member.role,
      active: member.active,
      superAdmin: member.superAdmin,
    });
    setMemberEdit({ role: member.role, active: member.active });
  };

  const openRemoveMember = (member: AdminMember | AdminUser) => {
    if (
      member.superAdmin ||
      member.userId === session?.user.id ||
      !member.active ||
      !member.organizationId ||
      !member.role
    ) {
      return;
    }
    setRemovingMember({
      userId: member.userId,
      organizationId: member.organizationId,
      email: member.email,
      firstName: member.firstName,
      lastName: member.lastName,
      role: member.role,
      active: member.active,
      superAdmin: member.superAdmin,
    });
  };

  const organizationColumns: Column<AdminOrganization>[] = [
    {
      key: "organization",
      header: "Organization",
      rowHeader: true,
      render: (organization) => (
        <div>
          <div className="font-semibold text-slate-950">{organization.name}</div>
        </div>
      ),
    },
    {
      key: "plan",
      header: "Current plan",
      render: (organization) => <Badge tone={planTone(organization.plan)}>{displayPlan(organization.plan)}</Badge>,
    },
    { key: "users", header: "Active users", render: (organization) => organization.memberCount },
    {
      key: "requests",
      header: "Join requests",
      render: (organization) => (
        <Badge tone={organization.pendingJoinRequestCount > 0 ? "warning" : "neutral"}>
          {organization.pendingJoinRequestCount}
        </Badge>
      ),
    },
    {
      key: "status",
      header: "Status",
      render: (organization) => (
        <Badge tone={organization.active ? "success" : "danger"}>
          {organization.active ? "Active" : "Disabled"}
        </Badge>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      align: "right",
      render: (organization) => (
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={() => setSelectedOrganizationID(organization.id)}>
            Access
          </Button>
          <Button variant="secondary" size="sm" onClick={() => openOrganizationSettings(organization)}>
            Edit
          </Button>
          <Button size="sm" disabled={!organization.active} onClick={() => openAddUser(organization.id)}>
            Add user
          </Button>
        </div>
      ),
    },
  ];

  const registeredUserColumns: Column<AdminUser>[] = [
    {
      key: "user",
      header: "Registered user",
      rowHeader: true,
      render: (user) => (
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-semibold text-slate-950">
              {[user.firstName, user.lastName].filter(Boolean).join(" ") || "Name not set"}
            </span>
          </div>
          <div className="mt-0.5 text-xs text-slate-500">{user.email}</div>
        </div>
      ),
    },
    {
      key: "organization",
      header: "Organization",
      render: (user) => user.organizationName || "Unassigned",
    },
    {
      key: "plan",
      header: "Current plan",
      render: (user) => <Badge tone={planTone(user.plan)}>{displayPlan(user.plan)}</Badge>,
    },
    {
      key: "role",
      header: "Access",
      render: (user) => displayRole(user.role),
    },
    {
      key: "status",
      header: "Status",
      render: (user) => <Badge tone={user.active ? "success" : "neutral"}>{user.active ? "Active" : "Disabled"}</Badge>,
    },
    {
      key: "actions",
      header: "Actions",
      align: "right",
      render: (user) => (
        <div className="flex justify-end gap-2">
          <Button
            variant="secondary"
            size="sm"
            disabled={!user.organizationId || !user.role}
            onClick={() => openMemberSettings(user)}
          >
            Edit access
          </Button>
          {user.active && !user.superAdmin && user.userId !== session?.user.id && (
            <Button variant="danger" size="sm" onClick={() => openRemoveMember(user)}>
              Remove user
            </Button>
          )}
        </div>
      ),
    },
  ];

  const memberColumns: Column<AdminMember>[] = [
    {
      key: "user",
      header: "User",
      rowHeader: true,
      render: (member) => (
        <div>
          <div className="font-semibold text-slate-950">
            {[member.firstName, member.lastName].filter(Boolean).join(" ") || "Name not set"}
          </div>
          <div className="mt-0.5 text-xs text-slate-500">{member.email}</div>
        </div>
      ),
    },
    { key: "role", header: "Role", render: (member) => displayRole(member.role) },
    {
      key: "status",
      header: "Status",
      render: (member) => (
        <Badge tone={member.active ? "success" : "neutral"}>{member.active ? "Active" : "Disabled"}</Badge>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      headerHidden: true,
      align: "right",
      render: (member) => (
        <div className="flex justify-end gap-2">
          <Button variant="secondary" size="sm" onClick={() => openMemberSettings(member)}>
            Edit access
          </Button>
          {member.active && !member.superAdmin && member.userId !== session?.user.id && (
            <Button variant="danger" size="sm" onClick={() => openRemoveMember(member)}>
              Remove user
            </Button>
          )}
        </div>
      ),
    },
  ];

  const joinRequestColumns: Column<OrganizationJoinRequest>[] = [
    {
      key: "applicant",
      header: "Applicant",
      rowHeader: true,
      render: (request) => (
        <div>
          <div className="font-semibold text-slate-950">
            {[request.firstName, request.lastName].filter(Boolean).join(" ") || "Name not set"}
          </div>
          <div className="mt-0.5 text-xs text-slate-500">{request.email}</div>
        </div>
      ),
    },
    {
      key: "requested",
      header: "Requested",
      render: (request) => new Date(request.requestedAt).toLocaleString(),
    },
    {
      key: "status",
      header: "Status",
      render: () => <Badge tone="warning">Pending</Badge>,
    },
    {
      key: "actions",
      header: "Actions",
      align: "right",
      render: (request) => (
        <div className="flex justify-end gap-2">
          <Button
            variant="secondary"
            size="sm"
            disabled={reviewJoinRequestMutation.isPending}
            onClick={() => reviewJoinRequestMutation.mutate({ request, decision: "reject" })}
          >
            Reject
          </Button>
          <Button
            size="sm"
            disabled={reviewJoinRequestMutation.isPending}
            onClick={() => {
              setApprovalRole("crm_member");
              setReviewingRequest(request);
            }}
          >
            Approve
          </Button>
        </div>
      ),
    },
  ];

  const copyRevealedJoinCode = async () => {
    if (!revealedJoinCode?.code) return;
    try {
      await navigator.clipboard.writeText(revealedJoinCode.code);
      toast.show({ tone: "success", message: "Organization code copied." });
    } catch {
      toast.show({ tone: "danger", message: "Copy failed. Select and copy the code manually." });
    }
  };

  const renderOrganizationAccess = (showOrganizationPicker: boolean) => {
    if (!selectedOrganization) return null;
    const oneTimeCode =
      revealedJoinCode?.organizationId === selectedOrganization.id ? revealedJoinCode.code : "";
    return (
      <section className="mt-7" aria-labelledby="organization-access-heading">
        <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
          <div>
            <h2 id="organization-access-heading" className="text-lg font-semibold text-slate-950">
              Organization access
            </h2>
            <p className="mt-1 text-sm text-slate-500">
              Share a code safely; every verified applicant still requires administrator approval.
            </p>
          </div>
          {showOrganizationPicker && (
            <Select
              label="Organization"
              options={organizationRows.map((organization) => ({ value: organization.id, label: organization.name }))}
              value={selectedOrganizationID}
              onChange={(event) => setSelectedOrganizationID(event.target.value)}
            />
          )}
        </div>

        <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
          <div className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
            <div>
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="font-semibold text-slate-950">Organization code</h3>
                <Badge tone={selectedOrganization.joinCodeEnabled ? "success" : "neutral"}>
                  {selectedOrganization.joinCodeHint
                    ? selectedOrganization.joinCodeEnabled
                      ? "Enabled"
                      : "Paused"
                    : "Not generated"}
                </Badge>
              </div>
              <p className="mt-1 text-sm text-slate-500">
                {selectedOrganization.joinCodeHint
                  ? `Stored securely · code ending ${selectedOrganization.joinCodeHint}`
                  : "Generate a high-entropy code before teammates can request access."}
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
              {selectedOrganization.joinCodeHint && (
                <Button
                  variant="secondary"
                  loading={toggleJoinCodeMutation.isPending}
                  onClick={() => toggleJoinCodeMutation.mutate(!selectedOrganization.joinCodeEnabled)}
                >
                  {selectedOrganization.joinCodeEnabled ? "Pause code" : "Enable code"}
                </Button>
              )}
              <Button loading={rotateJoinCodeMutation.isPending} onClick={() => rotateJoinCodeMutation.mutate()}>
                {selectedOrganization.joinCodeHint ? "Rotate code" : "Generate code"}
              </Button>
            </div>
          </div>

          {oneTimeCode && (
            <div className="mt-4 rounded-lg border border-brand-200 bg-brand-50 p-3" role="status">
              <p className="text-xs font-semibold uppercase tracking-wide text-brand-700">Copy this code now</p>
              <div className="mt-2 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                <code className="select-all text-base font-semibold tracking-wider text-slate-950">{oneTimeCode}</code>
                <Button variant="secondary" size="sm" onClick={() => void copyRevealedJoinCode()}>
                  Copy code
                </Button>
              </div>
              <p className="mt-2 text-xs text-slate-600">
                For security, the complete code is shown only after generation. Rotating it immediately invalidates the old code.
              </p>
            </div>
          )}
        </div>

        <div className="mt-5">
          <div className="mb-3 flex items-center justify-between gap-3">
            <div>
              <h3 className="font-semibold text-slate-950">Pending approval</h3>
              <p className="mt-1 text-sm text-slate-500">Review verified users before assigning CRM access.</p>
            </div>
            <Badge tone={(joinRequests.data?.length ?? 0) > 0 ? "warning" : "neutral"}>
              {joinRequests.data?.length ?? 0} pending
            </Badge>
          </div>
          {joinRequests.isError ? (
            <ErrorState description={messageFrom(joinRequests.error)} onRetry={() => void joinRequests.refetch()} />
          ) : joinRequests.isLoading ? (
            <div className="rounded-xl border border-slate-200 bg-white p-6 text-sm text-slate-500">
              Loading access requests…
            </div>
          ) : (joinRequests.data?.length ?? 0) === 0 ? (
            <EmptyState
              title="No pending requests"
              description="Verified requests submitted with this organization's code will appear here."
            />
          ) : (
            <Table
              caption={`Pending access requests for ${selectedOrganization.name}`}
              columns={joinRequestColumns}
              rows={joinRequests.data ?? []}
              getRowKey={(request) => request.id}
            />
          )}
        </div>
      </section>
    );
  };

  if (!allowed) {
    return (
      <div>
        <PageHeader title="Administration" />
        <EmptyState
          title="Administrator access required"
          description="Ask an organization administrator to assign your CRM role."
        />
      </div>
    );
  }

  const submit = (event: FormEvent, action: () => void) => {
    event.preventDefault();
    action();
  };

  const activeOrganizations = organizationRows.filter((organization) => organization.active).length;
  const paidOrganizations = organizationRows.filter(
    (organization) => organization.active && organization.plan !== "free",
  ).length;
  const activeUsers = userRows.filter((user) => user.active).length;
  return (
    <div>
      {superAdmin ? (
        <>
          <PageHeader
            title="Super Admin"
            actions={
              <>
                <Badge tone="brand">Protected account</Badge>
                <Button variant="secondary" disabled={organizationRows.length === 0} onClick={() => openAddUser()}>
                  Add user
                </Button>
                <Button onClick={() => setOrganizationDialog(true)}>Create organization</Button>
              </>
            }
          />
          <p className="mb-5 max-w-3xl text-sm leading-6 text-slate-600">
            Platform controls only. Review every registration, organization, plan, and access status without entering
            customer CRM records.
          </p>

          <dl className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            <Metric label="Registered users" value={userRows.length} detail={`${activeUsers} currently active`} />
            <Metric label="Organizations" value={organizationRows.length} detail={`${activeOrganizations} active`} />
            <Metric label="Paid plans" value={paidOrganizations} detail="Pro and Enterprise organizations" />
            <Metric
              label="Free plans"
              value={organizationRows.filter((organization) => organization.plan === "free").length}
              detail="Available for plan conversion"
            />
          </dl>

          <section className="mt-7" aria-labelledby="organizations-heading">
            <div className="mb-3">
              <h2 id="organizations-heading" className="text-lg font-semibold text-slate-950">
                Organizations
              </h2>
              <p className="mt-1 text-sm text-slate-500">Create workspaces, assign plans, and control availability.</p>
            </div>
            {organizations.isError ? (
              <ErrorState description={messageFrom(organizations.error)} onRetry={() => void organizations.refetch()} />
            ) : organizations.isLoading ? (
              <div className="rounded-xl border border-slate-200 bg-white p-6 text-sm text-slate-500">
                Loading organizations…
              </div>
            ) : organizationRows.length === 0 ? (
              <EmptyState
                title="No organizations yet"
                description="Create the first customer organization and its administrator."
                action={<Button onClick={() => setOrganizationDialog(true)}>Create organization</Button>}
              />
            ) : (
              <Table
                caption="All Savvy CRM organizations and their plans"
                columns={organizationColumns}
                rows={organizationRows}
                getRowKey={(organization) => organization.id}
              />
            )}
          </section>

          {renderOrganizationAccess(true)}

          <section className="mt-7" aria-labelledby="registered-users-heading">
            <div className="mb-3">
              <h2 id="registered-users-heading" className="text-lg font-semibold text-slate-950">
                Registered users
              </h2>
              <p className="mt-1 text-sm text-slate-500">
                Every customer account with its organization, current plan, role, and access status.
              </p>
            </div>
            {registeredUsers.isError ? (
              <ErrorState description={messageFrom(registeredUsers.error)} onRetry={() => void registeredUsers.refetch()} />
            ) : registeredUsers.isLoading ? (
              <div className="rounded-xl border border-slate-200 bg-white p-6 text-sm text-slate-500">
                Loading registered users…
              </div>
            ) : userRows.length === 0 ? (
              <EmptyState title="No registered users" description="New signups and granted users will appear here." />
            ) : (
              <Table
                caption="All registered Savvy CRM users"
                columns={registeredUserColumns}
                rows={userRows}
                getRowKey={(user) => user.userId}
              />
            )}
          </section>
        </>
      ) : (
        <>
          <PageHeader
            title="Organization administration"
            actions={<Badge tone="neutral">Workspace admin</Badge>}
          />
          <p className="mb-5 max-w-3xl text-sm leading-6 text-slate-600">
            Manage your organization profile and decide who can access this CRM workspace.
          </p>

          {organizations.isError ? (
            <ErrorState description={messageFrom(organizations.error)} onRetry={() => void organizations.refetch()} />
          ) : organizations.isLoading ? (
            <div className="rounded-xl border border-slate-200 bg-white p-6 text-sm text-slate-500">
              Loading organization…
            </div>
          ) : !selectedOrganization ? (
            <EmptyState title="Organization unavailable" description="Contact Savvy support to restore this workspace." />
          ) : (
            <>
              <section
                className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm"
                aria-labelledby="organization-heading"
              >
                <div className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
                  <div>
                    <p className="text-[10px] font-semibold uppercase tracking-[0.14em] text-brand-700">Organization</p>
                    <h2 id="organization-heading" className="mt-1 text-xl font-semibold text-slate-950">
                      {selectedOrganization.name}
                    </h2>
                  </div>
                  <Button variant="secondary" onClick={() => openOrganizationSettings(selectedOrganization)}>
                    Organization settings
                  </Button>
                </div>
                <dl className="mt-4 grid gap-3 border-t border-slate-100 pt-4 sm:grid-cols-3">
                  <div>
                    <dt className="text-xs text-slate-500">Plan</dt>
                    <dd className="mt-1 font-semibold text-slate-950">{displayPlan(selectedOrganization.plan)}</dd>
                  </div>
                  <div>
                    <dt className="text-xs text-slate-500">Active users</dt>
                    <dd className="mt-1 font-semibold text-slate-950">{selectedOrganization.memberCount}</dd>
                  </div>
                  <div>
                    <dt className="text-xs text-slate-500">Status</dt>
                    <dd className="mt-1">
                      <Badge tone={selectedOrganization.active ? "success" : "danger"}>
                        {selectedOrganization.active ? "Active" : "Disabled"}
                      </Badge>
                    </dd>
                  </div>
                </dl>
              </section>

              {renderOrganizationAccess(false)}

              <section className="mt-6" aria-labelledby="access-heading">
                <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
                  <div>
                    <h2 id="access-heading" className="text-lg font-semibold text-slate-950">
                      User access
                    </h2>
                    <p className="mt-1 text-sm text-slate-500">Grant access and assign a fixed CRM role.</p>
                  </div>
                  <Button onClick={() => openAddUser()} disabled={!selectedOrganization.active}>
                    Add user
                  </Button>
                </div>
                {members.isError ? (
                  <ErrorState description={messageFrom(members.error)} onRetry={() => void members.refetch()} />
                ) : members.isLoading ? (
                  <div className="rounded-xl border border-slate-200 bg-white p-6 text-sm text-slate-500">
                    Loading users…
                  </div>
                ) : (members.data?.length ?? 0) === 0 ? (
                  <EmptyState
                    title="No users yet"
                    description="Grant the first user access to this organization."
                    action={<Button onClick={() => openAddUser()}>Add user</Button>}
                  />
                ) : (
                  <Table
                    caption="Organization user access"
                    columns={memberColumns}
                    rows={members.data ?? []}
                    getRowKey={(member) => member.userId}
                  />
                )}
              </section>
            </>
          )}

          <section className="mt-8 border-t border-slate-200 pt-6" aria-labelledby="configuration-heading">
            <h2 id="configuration-heading" className="text-lg font-semibold text-slate-950">
              Workspace configuration
            </h2>
            <div className="mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
              {ADMIN_AREAS.map((area) => (
                <Link
                  key={area.to}
                  to={area.to}
                  className="rounded-xl border border-slate-200 bg-white p-3.5 transition hover:border-brand-300 hover:bg-brand-50/30 focus-visible:outline focus-visible:outline-2 focus-visible:outline-brand-600"
                >
                  <h3 className="text-sm font-semibold text-slate-950">{area.title}</h3>
                  <p className="mt-1 text-xs leading-5 text-slate-500">{area.description}</p>
                </Link>
              ))}
            </div>
          </section>
        </>
      )}

      <Dialog
        open={!!reviewingRequest}
        onClose={() => setReviewingRequest(null)}
        title="Approve organization access"
        footer={
          <>
            <Button variant="secondary" onClick={() => setReviewingRequest(null)}>
              Cancel
            </Button>
            <Button
              loading={reviewJoinRequestMutation.isPending}
              onClick={() => {
                if (reviewingRequest) {
                  reviewJoinRequestMutation.mutate({ request: reviewingRequest, decision: "approve", role: approvalRole });
                }
              }}
            >
              Approve access
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <div className="rounded-lg bg-slate-50 p-3">
            <p className="font-semibold text-slate-950">
              {[reviewingRequest?.firstName, reviewingRequest?.lastName].filter(Boolean).join(" ") || "Applicant"}
            </p>
            <p className="text-xs text-slate-500">{reviewingRequest?.email}</p>
          </div>
          <Select
            label="CRM role"
            options={ROLE_OPTIONS}
            value={approvalRole}
            onChange={(event) => setApprovalRole(event.target.value as CrmRole)}
          />
          <p className="text-xs leading-5 text-slate-500">
            Approval creates the membership immediately. The applicant can then log in using the same verified email.
          </p>
        </div>
      </Dialog>

      <Dialog
        open={organizationDialog}
        onClose={() => setOrganizationDialog(false)}
        title="Create organization"
        footer={
          <>
            <Button variant="secondary" onClick={() => setOrganizationDialog(false)}>
              Cancel
            </Button>
            <Button type="submit" form="create-organization-form" loading={createOrganizationMutation.isPending}>
              Create organization
            </Button>
          </>
        }
      >
        <form
          id="create-organization-form"
          className="space-y-4"
          onSubmit={(event) => submit(event, () => createOrganizationMutation.mutate())}
        >
          <Input
            label="Organization name"
            required
            value={organizationForm.name}
            onChange={(event) => setOrganizationForm((current) => ({ ...current, name: event.target.value }))}
          />
          <Select
            label="Plan"
            options={PLAN_OPTIONS}
            value={organizationForm.plan}
            onChange={(event) =>
              setOrganizationForm((current) => ({ ...current, plan: event.target.value as OrganizationPlan }))
            }
          />
          <div className="border-t border-slate-200 pt-4">
            <p className="mb-3 font-semibold text-slate-950">Initial administrator</p>
            <div className="space-y-4">
              <Input
                label="Work email"
                type="email"
                required
                value={organizationForm.adminEmail}
                onChange={(event) =>
                  setOrganizationForm((current) => ({ ...current, adminEmail: event.target.value }))
                }
              />
              <div className="grid gap-3 sm:grid-cols-2">
                <Input
                  label="First name"
                  value={organizationForm.adminFirstName}
                  onChange={(event) =>
                    setOrganizationForm((current) => ({ ...current, adminFirstName: event.target.value }))
                  }
                />
                <Input
                  label="Last name"
                  value={organizationForm.adminLastName}
                  onChange={(event) =>
                    setOrganizationForm((current) => ({ ...current, adminLastName: event.target.value }))
                  }
                />
              </div>
            </div>
          </div>
        </form>
      </Dialog>

      <Dialog
        open={editOrganizationDialog}
        onClose={() => setEditOrganizationDialog(false)}
        title="Organization settings"
        footer={
          <>
            <Button variant="secondary" onClick={() => setEditOrganizationDialog(false)}>
              Cancel
            </Button>
            <Button type="submit" form="edit-organization-form" loading={updateOrganizationMutation.isPending}>
              Save changes
            </Button>
          </>
        }
      >
        <form
          id="edit-organization-form"
          className="space-y-4"
          onSubmit={(event) => submit(event, () => updateOrganizationMutation.mutate())}
        >
          <Input
            label="Organization name"
            required
            value={organizationEdit.name}
            onChange={(event) => setOrganizationEdit((current) => ({ ...current, name: event.target.value }))}
          />
          {superAdmin && (
            <>
              <Select
                label="Plan"
                options={PLAN_OPTIONS}
                value={organizationEdit.plan}
                onChange={(event) =>
                  setOrganizationEdit((current) => ({ ...current, plan: event.target.value as OrganizationPlan }))
                }
              />
              <label className="flex items-center gap-3 rounded-lg border border-slate-200 p-3 font-medium text-slate-800">
                <input
                  type="checkbox"
                  checked={organizationEdit.active}
                  onChange={(event) =>
                    setOrganizationEdit((current) => ({ ...current, active: event.target.checked }))
                  }
                />
                Organization active
              </label>
            </>
          )}
        </form>
      </Dialog>

      <Dialog
        open={memberDialog}
        onClose={() => setMemberDialog(false)}
        title="Add user access"
        footer={
          <>
            <Button variant="secondary" onClick={() => setMemberDialog(false)}>
              Cancel
            </Button>
            <Button type="submit" form="create-member-form" loading={createMemberMutation.isPending}>
              Grant access
            </Button>
          </>
        }
      >
        <form
          id="create-member-form"
          className="space-y-4"
          onSubmit={(event) => submit(event, () => createMemberMutation.mutate())}
        >
          {superAdmin && (
            <Select
              label="Organization"
              options={organizationRows.map((organization) => ({
                value: organization.id,
                label: `${organization.name}${organization.active ? "" : " (disabled)"}`,
              }))}
              value={selectedOrganizationID}
              onChange={(event) => setSelectedOrganizationID(event.target.value)}
            />
          )}
          <Input
            label="Work email"
            type="email"
            required
            value={memberForm.email}
            hint="The user will sign in with an email OTP or their linked Google account."
            onChange={(event) => setMemberForm((current) => ({ ...current, email: event.target.value }))}
          />
          <div className="grid gap-3 sm:grid-cols-2">
            <Input
              label="First name"
              value={memberForm.firstName}
              onChange={(event) => setMemberForm((current) => ({ ...current, firstName: event.target.value }))}
            />
            <Input
              label="Last name"
              value={memberForm.lastName}
              onChange={(event) => setMemberForm((current) => ({ ...current, lastName: event.target.value }))}
            />
          </div>
          <Select
            label="CRM role"
            options={ROLE_OPTIONS}
            value={memberForm.role}
            onChange={(event) => setMemberForm((current) => ({ ...current, role: event.target.value as CrmRole }))}
          />
        </form>
      </Dialog>

      <Dialog
        open={!!editingMember}
        onClose={() => setEditingMember(null)}
        title="Edit user access"
        footer={
          <>
            <Button variant="secondary" onClick={() => setEditingMember(null)}>
              Cancel
            </Button>
            <Button type="submit" form="edit-member-form" loading={updateMemberMutation.isPending}>
              Save access
            </Button>
          </>
        }
      >
        <form
          id="edit-member-form"
          className="space-y-4"
          onSubmit={(event) => submit(event, () => updateMemberMutation.mutate())}
        >
          <div className="rounded-lg bg-slate-50 p-3">
            <p className="font-semibold text-slate-950">
              {[editingMember?.firstName, editingMember?.lastName].filter(Boolean).join(" ") || "Name not set"}
            </p>
            <p className="text-xs text-slate-500">{editingMember?.email}</p>
          </div>
          <Select
            label="CRM role"
            options={ROLE_OPTIONS}
            value={memberEdit.role}
            onChange={(event) => setMemberEdit((current) => ({ ...current, role: event.target.value as CrmRole }))}
          />
          <label className="flex items-center gap-3 rounded-lg border border-slate-200 p-3 font-medium text-slate-800">
            <input
              type="checkbox"
              checked={memberEdit.active}
              onChange={(event) => setMemberEdit((current) => ({ ...current, active: event.target.checked }))}
            />
            User can access this organization
          </label>
        </form>
      </Dialog>

      <Dialog
        open={!!removingMember}
        onClose={() => setRemovingMember(null)}
        title="Remove user access"
        footer={
          <>
            <Button variant="secondary" onClick={() => setRemovingMember(null)}>
              Cancel
            </Button>
            <Button
              variant="danger"
              loading={removeMemberMutation.isPending}
              onClick={() => {
                if (removingMember) removeMemberMutation.mutate(removingMember);
              }}
            >
              Remove access
            </Button>
          </>
        }
      >
        <div className="space-y-3">
          <div className="rounded-lg bg-slate-50 p-3">
            <p className="font-semibold text-slate-950">
              {[removingMember?.firstName, removingMember?.lastName].filter(Boolean).join(" ") || "User"}
            </p>
            <p className="text-xs text-slate-500">{removingMember?.email}</p>
          </div>
          <p>
            This user will immediately lose access to the organization. Their CRM records and administration audit
            history will be retained, and an administrator can restore access later.
          </p>
        </div>
      </Dialog>
    </div>
  );
}

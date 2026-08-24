import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AutomationPage from "./AutomationPage";
import { renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    describeWorkflowCatalog: vi.fn(),
    listWorkflowRules: vi.fn(),
    getWorkflowRule: vi.fn(),
    createWorkflowRule: vi.fn(),
    updateWorkflowRule: vi.fn(),
    publishWorkflowRuleVersion: vi.fn(),
    setWorkflowRuleStatus: vi.fn(),
    listWorkflowExecutions: vi.fn(),
  },
  auth: {
    hasScope: vi.fn(),
    hasRole: vi.fn(),
    status: "authenticated",
    session: { claims: { sub: "user-1" } },
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [{ path: "/settings/automation", element: <AutomationPage /> }];

/** The catalog as the server really reports it: text and number fields carry
 * different operator sets, which is the whole reason the editor reads it
 * rather than holding its own list. */
const catalog = {
  triggers: [
    {
      eventType: "lead.created", objectType: "lead",
      fields: [
        { name: "source", kind: "text", operators: ["eq", "neq", "contains", "in"] },
        { name: "score", kind: "number", operators: ["eq", "neq", "gt", "lt"] },
      ],
    },
    {
      eventType: "opportunity.stage_changed", objectType: "opportunity",
      fields: [{ name: "stage", kind: "text", operators: ["eq", "neq", "contains", "in"] }],
    },
  ],
  actionTypes: ["create_task", "add_activity", "notify"],
  assigneeSelectors: ["record_owner", "fixed_user"],
  maxConditions: 10, maxActions: 10, maxTextLength: 500,
};

const activeRule = {
  id: "rule-1", name: "Referral follow-up", description: "Call new referrals",
  status: "active", activeVersionId: "ver-1", createdBy: "user-1",
};

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.describeWorkflowCatalog.mockResolvedValue(catalog);
  crm.listWorkflowRules.mockResolvedValue({ rules: [activeRule] });
  crm.listWorkflowExecutions.mockResolvedValue({ executions: [] });
  crm.createWorkflowRule.mockResolvedValue({ rule: { id: "rule-2" } });
  crm.updateWorkflowRule.mockResolvedValue({ rule: { id: "rule-1" } });
  crm.publishWorkflowRuleVersion.mockResolvedValue({ version: { id: "ver-2" }, rule: activeRule });
  crm.setWorkflowRuleStatus.mockResolvedValue({ rule: activeRule });
  crm.getWorkflowRule.mockResolvedValue({
    rule: activeRule,
    activeVersion: {
      id: "ver-1", ruleId: "rule-1", version: 1, triggerEventType: "lead.created",
      conditions: [{ field: "source", operator: "eq", value: "referral" }],
      actions: [{
        type: "create_task", title: "Call the referral", body: "", assignee: "record_owner",
        assigneeId: "", priority: "medium", dueInDays: 1, activityKind: "",
      }],
    },
  });
});

async function loaded() {
  const view = renderRoutes(routes, ["/settings/automation"]);
  await screen.findByRole("button", { name: "History for Referral follow-up" });
  return view;
}

describe("AutomationPage accessibility", () => {
  it("has no violations on the loaded page", async () => {
    const { container } = await loaded();
    await expectNoA11yViolations(container);
  });

  it("has no violations with the rule editor open", async () => {
    const { container } = await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New rule" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });
});

describe("AutomationPage", () => {
  it("shows each rule's lifecycle state", async () => {
    await loaded();
    expect(screen.getByText("active")).toBeInTheDocument();
  });

  // The point of the catalog: the editor must not invent an operator the
  // server would refuse. A number field offers comparisons a text one does
  // not, and vice versa.
  it("offers only the operators the field's kind accepts", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New rule" }));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Add a condition" }));

    const comparison = await screen.findByLabelText("Comparison 1");
    expect(within(comparison).getByRole("option", { name: "contains" })).toBeInTheDocument();
    expect(within(comparison).queryByRole("option", { name: "is greater than" })).toBeNull();

    fireEvent.change(screen.getByLabelText("Field 1"), { target: { value: "score" } });
    await waitFor(() =>
      expect(within(screen.getByLabelText("Comparison 1")).getByRole("option", { name: "is greater than" }))
        .toBeInTheDocument());
    expect(within(screen.getByLabelText("Comparison 1")).queryByRole("option", { name: "contains" })).toBeNull();
  });

  // A rule saved without switching on must publish with activate=false, or
  // an author's unreviewed edit starts acting on records immediately.
  it("saves a version without switching it on", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New rule" }));
    await screen.findByRole("dialog");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Referral triage" } });
    fireEvent.change(screen.getByLabelText("Title 1"), { target: { value: "Call them" } });
    fireEvent.click(screen.getByRole("button", { name: "Save without switching on" }));

    await waitFor(() => expect(crm.publishWorkflowRuleVersion).toHaveBeenCalled());
    expect(crm.publishWorkflowRuleVersion.mock.calls[0][0]).toMatchObject({
      ruleId: "rule-2", triggerEventType: "lead.created", activate: false,
    });
  });

  it("publishes and activates when the author asks for it", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "New rule" }));
    await screen.findByRole("dialog");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Referral triage" } });
    fireEvent.change(screen.getByLabelText("Title 1"), { target: { value: "Call them" } });
    fireEvent.click(screen.getByRole("button", { name: "Save and switch on" }));

    await waitFor(() => expect(crm.publishWorkflowRuleVersion).toHaveBeenCalled());
    expect(crm.publishWorkflowRuleVersion.mock.calls[0][0]).toMatchObject({ activate: true });
  });

  // Editing loads the running version rather than starting blank: an author
  // correcting one condition must not silently drop the other three.
  it("opens an existing rule with its published version filled in", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Edit Referral follow-up" }));
    await screen.findByRole("dialog");

    expect(await screen.findByDisplayValue("Referral follow-up")).toBeInTheDocument();
    expect(screen.getByDisplayValue("referral")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Call the referral")).toBeInTheDocument();
  });

  it("pauses a running rule", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Pause Referral follow-up" }));
    await waitFor(() => expect(crm.setWorkflowRuleStatus)
      .toHaveBeenCalledWith({ id: "rule-1", status: "paused" }));
  });

  // Archiving is permanent, so it goes through a confirmation that says so.
  it("confirms before archiving, and says archiving cannot be undone", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Archive Referral follow-up" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/cannot be switched back on/)).toBeInTheDocument();
    expect(crm.setWorkflowRuleStatus).not.toHaveBeenCalled();

    fireEvent.click(within(dialog).getByRole("button", { name: "Archive" }));
    await waitFor(() => expect(crm.setWorkflowRuleStatus)
      .toHaveBeenCalledWith({ id: "rule-1", status: "archived" }));
  });

  // "Why didn't my rule fire" is the question the ledger exists to answer, so
  // a skipped run has to read as a decision rather than a blank.
  it("explains a skipped run and a failed one in the history", async () => {
    crm.listWorkflowExecutions.mockResolvedValue({
      executions: [
        {
          id: "ex-1", ruleId: "rule-1", sourceEventType: "lead.created",
          status: "skipped", attempts: 0, errorCategory: "",
        },
        {
          id: "ex-2", ruleId: "rule-1", sourceEventType: "lead.created",
          status: "failed", attempts: 1, errorCategory: "no_assignee",
        },
      ],
    });
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "History for Referral follow-up" }));
    const dialog = await screen.findByRole("dialog");

    expect(await within(dialog).findByText("Its conditions did not hold.")).toBeInTheDocument();
    expect(within(dialog).getByText(/Nobody to assign the work to/)).toBeInTheDocument();
  });

  // A reader without authoring authority still gets the list and the history;
  // what they must not get is a control that will be refused.
  it("hides the editing controls from a reader", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope === "crm:workflow:read");
    await loaded();
    expect(screen.queryByRole("button", { name: "New rule" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit Referral follow-up" })).toBeNull();
    expect(screen.getByRole("button", { name: "History for Referral follow-up" })).toBeInTheDocument();
  });

  it("tells a user with no automation scope why the page is empty", async () => {
    auth.hasScope.mockReturnValue(false);
    renderRoutes(routes, ["/settings/automation"]);
    expect(await screen.findByText("Automation access required")).toBeInTheDocument();
    expect(crm.listWorkflowRules).not.toHaveBeenCalled();
  });
});

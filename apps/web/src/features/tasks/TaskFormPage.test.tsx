import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import TaskFormPage from "./TaskFormPage";
import { makeTask, makeTimestamp, renderRoutes, sampleCustomFields } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { getTask: vi.fn(), createTask: vi.fn(), updateTask: vi.fn() },
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

const savedAt = makeTimestamp("2026-08-21T10:00:00Z");

const routes = [
  { path: "/tasks", element: <p>Tasks list</p> },
  { path: "/tasks/:id/edit", element: <TaskFormPage mode="edit" /> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getTask.mockResolvedValue({ task: makeTask({ updatedAt: savedAt }) });
  crm.updateTask.mockResolvedValue({ task: makeTask() });
});

describe("TaskFormPage", () => {
  // The two fields the form has no input for and would drop by omission.
  // updated_at is the version the edit was opened on; custom_fields is a
  // document UpdateTask replaces rather than merges, so a save without it
  // deletes every custom value on the task — and fails outright where the
  // tenant has marked one required.
  it("round-trips the fields it does not expose", async () => {
    renderRoutes(routes, ["/tasks/task-1/edit"]);
    await waitFor(() => expect(screen.getByLabelText(/^Title/)).toHaveValue("Send the proposal"));

    fireEvent.change(screen.getByLabelText(/^Title/), { target: { value: "Send the revised proposal" } });
    fireEvent.click(screen.getByRole("button", { name: /^Save/ }));

    await waitFor(() => expect(crm.updateTask).toHaveBeenCalledTimes(1));
    const sent = crm.updateTask.mock.calls[0][0].task;
    expect(sent).toMatchObject({ id: "task-1", title: "Send the revised proposal" });
    expect(sent.updatedAt).toEqual(savedAt);
    expect(sent.customFields).toEqual(sampleCustomFields);
  });

  // A form is where label association and error-to-field wiring live, and
  // both are invisible until something reads the page aloud.
  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes, ["/tasks/task-1/edit"]);
    await waitFor(() => expect(screen.getByLabelText(/^Title/)).toHaveValue("Send the proposal"));
    await expectNoA11yViolations(container);
  });

});

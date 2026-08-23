import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { TasksPanel } from "./TasksPanel";
import { makeTask, renderRoutes } from "../test/harness";
import { expectNoA11yViolations } from "../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listTasks: vi.fn(), completeTask: vi.fn() },
  auth: {
    hasScope: vi.fn(),
    hasRole: vi.fn(),
    status: "authenticated",
    session: { claims: { sub: "user-1" } },
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [
  { path: "/", element: <TasksPanel subjectType="account" subjectId="acc-1" /> },
  { path: "/tasks/new", element: <p>New task form</p> },
  { path: "/tasks/:id/edit", element: <p>Task form</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.listTasks.mockResolvedValue({
    tasks: [makeTask({ subjectType: "account", subjectId: "acc-1" })],
    nextPageToken: "",
  });
  crm.completeTask.mockResolvedValue({ task: makeTask({ status: "completed" }) });
});

describe("TasksPanel", () => {
  // A record page answers "what is outstanding here" — a completed task
  // belongs to the history, and the full list lives on /tasks.
  it("asks only for the record's open tasks", async () => {
    renderRoutes(routes);
    await screen.findByText("Send the proposal");

    expect(crm.listTasks).toHaveBeenCalledWith(
      expect.objectContaining({ subjectType: "account", subjectId: "acc-1", openOnly: true }),
    );
  });

  it("carries the record through to the create form", async () => {
    const { router } = renderRoutes(routes);
    await screen.findByText("Send the proposal");

    fireEvent.click(screen.getByRole("button", { name: "Add task" }));

    await screen.findByText("New task form");
    // Without this the task would be created unattached, and the panel it
    // was created from would not show it.
    expect(router.state.location.search).toContain("subjectType=account");
    expect(router.state.location.search).toContain("subjectId=acc-1");
  });

  it("completes a task in place", async () => {
    renderRoutes(routes);
    await screen.findByText("Send the proposal");

    fireEvent.click(screen.getByRole("button", { name: "Complete" }));

    await waitFor(() => expect(crm.completeTask).toHaveBeenCalledWith({ id: "task-1" }));
  });

  it("says so when a task has no due date rather than showing a placeholder", async () => {
    renderRoutes(routes);
    expect(await screen.findByText("No due date")).toBeInTheDocument();
  });

  it("marks a past due date on an open task", async () => {
    const yesterday = new Date(Date.now() - 86_400_000);
    crm.listTasks.mockResolvedValue({
      tasks: [makeTask({ dueAt: timestampFromDate(yesterday) })],
      nextPageToken: "",
    });
    renderRoutes(routes);

    const due = await screen.findByText(`Due ${yesterday.toLocaleDateString()}`);
    expect(due.className).toContain("text-rose-600");
  });

  it("offers no write actions without the task write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:task:write");
    renderRoutes(routes);
    await screen.findByText("Send the proposal");

    expect(screen.queryByRole("button", { name: "Add task" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Complete" })).not.toBeInTheDocument();
  });

  it("says nothing is outstanding rather than showing an empty box", async () => {
    crm.listTasks.mockResolvedValue({ tasks: [], nextPageToken: "" });
    renderRoutes(routes);
    expect(await screen.findByText("Nothing outstanding.")).toBeInTheDocument();
  });

  // Static a11y over an embedded panel with its own dialog and row controls.
  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes);
    await screen.findByText("Send the proposal");
    await expectNoA11yViolations(container);
  });

});

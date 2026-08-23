import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import TasksListPage from "./TasksListPage";
import { makeTask, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listTasks: vi.fn(), completeTask: vi.fn(), reopenTask: vi.fn() },
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

const routes = [
  { path: "/tasks", element: <TasksListPage /> },
  { path: "/tasks/new", element: <p>New task form</p> },
  { path: "/tasks/:id/edit", element: <p>Task form</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listTasks.mockResolvedValue({ tasks: [makeTask()], nextPageToken: "" });
  crm.completeTask.mockResolvedValue({ task: makeTask({ status: "completed" }) });
  crm.reopenTask.mockResolvedValue({ task: makeTask({ status: "open" }) });
});

function lastListRequest() {
  const calls = crm.listTasks.mock.calls;
  return calls[calls.length - 1][0];
}

describe("TasksListPage", () => {
  it("opens on the reader's own open tasks", async () => {
    renderRoutes(routes, ["/tasks"]);
    await screen.findByText("Send the proposal");

    // The question asked every morning, and the reason "All tasks" is not
    // the default: landing on the whole organization's list buries your own.
    const request = lastListRequest();
    expect(request.assigneeId).toBe("user-1");
    expect(request.openOnly).toBe(true);
  });

  it("asks the server for overdue tasks rather than filtering client-side", async () => {
    renderRoutes(routes, ["/tasks"]);
    await screen.findByText("Send the proposal");

    fireEvent.click(screen.getByRole("tab", { name: "Overdue" }));

    await waitFor(() => {
      const request = lastListRequest();
      expect(request.openOnly).toBe(true);
      expect(request.dueBefore).toBeDefined();
    });
  });

  // A page token belongs to the query that produced it. Carrying one across a
  // filter change asks the server to resume a list that no longer exists.
  it("drops the page cursor when the view changes", async () => {
    crm.listTasks.mockResolvedValue({ tasks: [makeTask()], nextPageToken: "page-2" });
    renderRoutes(routes, ["/tasks"]);
    await screen.findByText("Send the proposal");

    fireEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() => expect(lastListRequest().pageToken).toBe("page-2"));

    fireEvent.click(screen.getByRole("tab", { name: "All tasks" }));
    await waitFor(() => expect(lastListRequest().pageToken).toBe(""));
  });

  // Completing from the list uses CompleteTask, not a whole-record update:
  // ticking a box must not be able to lose somebody's concurrent edit.
  it("completes a task through the dedicated call", async () => {
    renderRoutes(routes, ["/tasks"]);
    await screen.findByText("Send the proposal");

    fireEvent.click(screen.getByRole("checkbox", { name: /complete send the proposal/i }));

    await waitFor(() => expect(crm.completeTask).toHaveBeenCalledWith({ id: "task-1" }));
  });

  it("reopens a completed task from the same control", async () => {
    crm.listTasks.mockResolvedValue({
      tasks: [makeTask({ status: "completed" })],
      nextPageToken: "",
    });
    renderRoutes(routes, ["/tasks"]);
    await screen.findByText("Send the proposal");

    fireEvent.click(screen.getByRole("checkbox", { name: /reopen send the proposal/i }));

    await waitFor(() => expect(crm.reopenTask).toHaveBeenCalledWith({ id: "task-1" }));
  });

  it("marks an overdue task without needing the server to say so", async () => {
    crm.listTasks.mockResolvedValue({
      tasks: [makeTask({ dueAt: timestampFromDate(new Date(Date.now() - 86_400_000)) })],
      nextPageToken: "",
    });
    renderRoutes(routes, ["/tasks"]);

    const due = await screen.findByText(new Date(Date.now() - 86_400_000).toLocaleDateString());
    expect(due.className).toContain("text-rose-600");
  });

  // A completed task is finished; colouring its past due date red would say
  // the opposite of what happened.
  it("does not mark a completed task overdue", async () => {
    crm.listTasks.mockResolvedValue({
      tasks: [
        makeTask({ status: "completed", dueAt: timestampFromDate(new Date(Date.now() - 86_400_000)) }),
      ],
      nextPageToken: "",
    });
    renderRoutes(routes, ["/tasks"]);

    const due = await screen.findByText(new Date(Date.now() - 86_400_000).toLocaleDateString());
    expect(due.className).not.toContain("text-rose-600");
  });

  it("hides the completion control and the create action without write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:task:write");
    renderRoutes(routes, ["/tasks"]);
    await screen.findByText("Send the proposal");

    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New task" })).not.toBeInTheDocument();
  });

  it("explains an empty overdue view differently from an empty list", async () => {
    crm.listTasks.mockResolvedValue({ tasks: [], nextPageToken: "" });
    renderRoutes(routes, ["/tasks"]);

    expect(await screen.findByText("No open tasks assigned to you")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("tab", { name: "Overdue" }));
    expect(await screen.findByText("Nothing overdue")).toBeInTheDocument();
  });

  // Static a11y over the tasks table, whose action column carries real controls.
  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes, ["/tasks"]);
    await screen.findByRole("table");
    await expectNoA11yViolations(container);
  });

});

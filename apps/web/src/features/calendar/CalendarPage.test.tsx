import { fireEvent, screen, waitFor } from "@testing-library/react";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { beforeEach, describe, expect, it, vi } from "vitest";
import CalendarPage from "./CalendarPage";
import { makeTask, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listTasks: vi.fn(), createTask: vi.fn() },
  auth: { hasScope: vi.fn(), session: { claims: { sub: "user-1" } } },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [
  { path: "/calendar", element: <CalendarPage /> },
  { path: "/tasks/new", element: <p>New task</p> },
  { path: "/tasks/:id/edit", element: <p>Edit task</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.listTasks.mockResolvedValue({
    tasks: [makeTask({ title: "Prepare renewal", dueAt: timestampFromDate(new Date()) })],
    nextPageToken: "",
  });
  crm.createTask.mockResolvedValue({ task: makeTask({ id: "task-2" }) });
});

describe("CalendarPage", () => {
  it("shows due-dated tasks and requests the calendar task set", async () => {
    renderRoutes(routes, ["/calendar"]);
    expect(await screen.findByText("Prepare renewal")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download .ics" })).toBeInTheDocument();
    expect(crm.listTasks).toHaveBeenCalledWith({ pageSize: 500 });
  });

  it("creates a task directly on the selected calendar date", async () => {
    renderRoutes(routes, ["/calendar"]);
    await screen.findByText("Prepare renewal");

    fireEvent.click(screen.getAllByRole("button", { name: /Add task on/ })[0]);
    fireEvent.change(screen.getByLabelText("Task title"), { target: { value: "Confirm agenda" } });
    fireEvent.click(screen.getByRole("button", { name: "Create task" }));

    await waitFor(() => expect(crm.createTask).toHaveBeenCalledTimes(1));
    const sent = crm.createTask.mock.calls[0][0].task;
    expect(sent).toMatchObject({
      title: "Confirm agenda",
      status: "open",
      priority: "normal",
      assigneeId: "user-1",
    });
    expect(timestampDate(sent.dueAt).getHours()).toBe(23);
    expect(timestampDate(sent.dueAt).getMinutes()).toBe(59);
  });

  it("switches between month, week, and day views", async () => {
    renderRoutes(routes, ["/calendar"]);
    await screen.findByText("Prepare renewal");

    fireEvent.click(screen.getByRole("button", { name: "Week" }));
    expect(screen.getByRole("button", { name: "Week" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Prepare renewal")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Day" }));
    expect(screen.getByRole("button", { name: "Day" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Prepare renewal")).toBeInTheDocument();
  });

  it("filters tasks by priority, status, and assignee", async () => {
    crm.listTasks.mockResolvedValue({
      tasks: [
        makeTask({
          title: "Urgent follow-up",
          priority: "urgent",
          assigneeId: "user-2",
          dueAt: timestampFromDate(new Date()),
        }),
        makeTask({
          id: "task-2",
          title: "Completed review",
          status: "completed",
          dueAt: timestampFromDate(new Date()),
        }),
      ],
      nextPageToken: "",
    });
    renderRoutes(routes, ["/calendar"]);
    expect(await screen.findByText("Urgent follow-up")).toBeInTheDocument();
    expect(screen.queryByText("Completed review")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "normal" } });
    expect(screen.queryByText("Urgent follow-up")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Priority"), { target: { value: "all" } });
    fireEvent.change(screen.getByLabelText("Assignee"), { target: { value: "user-2" } });
    expect(screen.getByText("Urgent follow-up")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Assignee"), { target: { value: "all" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "completed" } });
    expect(screen.getByText("Completed review")).toBeInTheDocument();
    expect(screen.queryByText("Urgent follow-up")).not.toBeInTheDocument();
  });

  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes, ["/calendar"]);
    await waitFor(() => expect(screen.getByText("Prepare renewal")).toBeInTheDocument());
    await expectNoA11yViolations(container);
  });
});

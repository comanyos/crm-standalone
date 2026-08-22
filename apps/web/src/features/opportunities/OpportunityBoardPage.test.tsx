import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectError, Code } from "@connectrpc/connect";
import OpportunityBoardPage from "./OpportunityBoardPage";
import { makeOpportunity, makeSalesStages, makeTimestamp, renderRoutes } from "../../test/harness";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listPipelines: vi.fn(), listPipelineStages: vi.fn(), listOpportunities: vi.fn(), updateOpportunity: vi.fn() },
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
  { path: "/opportunities/board", element: <OpportunityBoardPage /> },
  { path: "/opportunities/:id", element: <p>Opportunity detail</p> },
];

const version = makeTimestamp("2026-03-01T10:00:00Z");

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listPipelines.mockResolvedValue({
    pipelines: [{ $typeName: "crm.v1.Pipeline", id: "system-sales", name: "Sales", isDefault: true }],
  });
  crm.listPipelineStages.mockResolvedValue({ stages: makeSalesStages() });
  crm.listOpportunities.mockResolvedValue({
    opportunities: [makeOpportunity({ updatedAt: version })],
    nextPageToken: "",
  });
  crm.updateOpportunity.mockResolvedValue({ opportunity: makeOpportunity({ stageId: "qualification" }) });
});

/** Opens a card's Move menu — the keyboard/touch path, and the one a test can
 * drive without jsdom's absent drag implementation. */
async function openMoveMenu(name = "Acme renewal") {
  renderRoutes(routes, ["/opportunities/board"]);
  await screen.findByText(name);
  fireEvent.click(screen.getByRole("button", { name: /^Move/ }));
  return screen.getByRole("list", { name: `Move ${name} to` });
}

function sentOpportunity() {
  return crm.updateOpportunity.mock.calls[0][0].opportunity;
}

describe("OpportunityBoardPage", () => {
  it("offers only the next open stage and the two closing stages", async () => {
    const menu = await openMoveMenu();
    const items = Array.from(menu.querySelectorAll("button")).map((el) => el.textContent);
    expect(items).toEqual(["Qualification", "Closed won (closes the deal)", "Closed lost (closes the deal)"]);
  });

  it("advances a deal one stage without prompting for anything", async () => {
    const menu = await openMoveMenu();
    fireEvent.click(menu.querySelector("button")!);

    await waitFor(() => expect(crm.updateOpportunity).toHaveBeenCalledTimes(1));
    expect(sentOpportunity().stageId).toBe("qualification");
  });

  // The three fields a partial write would quietly destroy. customFields is
  // replaced rather than merged by UpdateOpportunity, close_date is stamped by
  // the server only on the closing write, and updated_at is what makes the
  // concurrency check fire instead of a silent overwrite.
  it("round-trips the fields a stage move must not disturb", async () => {
    const menu = await openMoveMenu();
    fireEvent.click(menu.querySelector("button")!);

    await waitFor(() => expect(crm.updateOpportunity).toHaveBeenCalled());
    const sent = sentOpportunity();
    expect(sent.customFields).toEqual(makeOpportunity().customFields);
    expect(sent.updatedAt).toEqual(version);
    expect(sent.name).toBe("Acme renewal");
    expect(sent.amount).toBe(50000);
  });

  it("requires a close reason before a closing move, and omits the close date", async () => {
    const menu = await openMoveMenu();
    fireEvent.click(screen.getByRole("button", { name: /Closed won/ }));

    // The dialog gates the write: nothing is sent until a reason exists.
    expect(crm.updateOpportunity).not.toHaveBeenCalled();
    const confirm = await screen.findByRole("button", { name: "Close deal" });
    expect(confirm).toBeDisabled();

    fireEvent.change(screen.getByLabelText(/Close reason/), { target: { value: "signed the MSA" } });
    fireEvent.click(screen.getByRole("button", { name: "Close deal" }));

    await waitFor(() => expect(crm.updateOpportunity).toHaveBeenCalledTimes(1));
    const sent = sentOpportunity();
    expect(sent.stageId).toBe("closed_won");
    expect(sent.closeReason).toBe("signed the MSA");
    expect(sent.closeDate).toBeUndefined();
    expect(menu).not.toBeInTheDocument();
  });

  it("refuses a skipped stage without asking the server", async () => {
    renderRoutes(routes, ["/opportunities/board"]);
    await screen.findByText("Acme renewal");

    // A drop onto Proposal from Prospecting — two stages on. The server would
    // reject it; the board should not spend a request finding that out.
    const proposal = screen.getByRole("region", { name: /^Proposal,/ });
    fireEvent.dragStart(screen.getByText("Acme renewal").closest("[draggable]")!, {
      dataTransfer: { setData: vi.fn(), getData: () => "opp-1" },
    });
    fireEvent.drop(proposal, { dataTransfer: { getData: () => "opp-1" } });

    await screen.findByText(/Proposal is not the next stage/);
    expect(crm.updateOpportunity).not.toHaveBeenCalled();
  });

  it("moves the card on a legal drop", async () => {
    renderRoutes(routes, ["/opportunities/board"]);
    await screen.findByText("Acme renewal");

    const card = screen.getByText("Acme renewal").closest("[draggable]")!;
    fireEvent.dragStart(card, { dataTransfer: { setData: vi.fn(), getData: () => "opp-1" } });
    fireEvent.drop(screen.getByRole("region", { name: /^Qualification,/ }), {
      dataTransfer: { getData: () => "opp-1" },
    });

    await waitFor(() => expect(crm.updateOpportunity).toHaveBeenCalledTimes(1));
    expect(sentOpportunity().stageId).toBe("qualification");
  });

  // The reason the board sends updated_at at all: a board left open while
  // someone else edits the deal must not silently overwrite their work.
  it("reports a conflict rather than retrying the overwrite", async () => {
    crm.updateOpportunity.mockRejectedValue(new ConnectError("stale", Code.Aborted));
    const menu = await openMoveMenu();
    fireEvent.click(menu.querySelector("button")!);

    await screen.findByText(/changed by someone else/);
    expect(crm.updateOpportunity).toHaveBeenCalledTimes(1);
  });

  // The pending slot is what makes a drop feel immediate. If a failed write
  // left it set, the board would keep showing the deal in a column it never
  // reached — a lie that survives until the next refetch.
  it("puts the card back in its real column when the move fails", async () => {
    crm.updateOpportunity.mockRejectedValue(new ConnectError("stale", Code.Aborted));
    const menu = await openMoveMenu();
    fireEvent.click(menu.querySelector("button")!);

    await screen.findByText(/changed by someone else/);
    await waitFor(() => {
      const prospecting = screen.getByRole("region", { name: /^Prospecting,/ });
      expect(prospecting.textContent).toContain("Acme renewal");
    });
    expect(screen.getByRole("region", { name: /^Qualification,/ }).textContent).not.toContain("Acme renewal");
  });

  it("offers no way out of a closed deal", async () => {
    crm.listOpportunities.mockResolvedValue({
      opportunities: [makeOpportunity({ stageId: "closed_won", stage: "closed_won", closeReason: "signed" })],
      nextPageToken: "",
    });
    renderRoutes(routes, ["/opportunities/board"]);
    await screen.findByText("Acme renewal");

    expect(screen.queryByRole("button", { name: /^Move/ })).not.toBeInTheDocument();
    expect(screen.getByText("Acme renewal").closest("[draggable=true]")).toBeNull();
  });

  it("gives a reader no move controls at all", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:opportunity:write");
    renderRoutes(routes, ["/opportunities/board"]);
    await screen.findByText("Acme renewal");

    expect(screen.queryByRole("button", { name: /^Move/ })).not.toBeInTheDocument();
    expect(screen.getByText("Acme renewal").closest("[draggable=true]")).toBeNull();
  });
});

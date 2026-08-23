import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import PipelinesPage from "./PipelinesPage";
import { Code, ConnectError } from "@connectrpc/connect";
import { makePipeline, makeSalesStages, makeTimestamp, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: {
    listPipelines: vi.fn(),
    listPipelineStages: vi.fn(),
    createPipeline: vi.fn(),
    createPipelineStage: vi.fn(),
    updatePipeline: vi.fn(),
    updatePipelineStage: vi.fn(),
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

const routes = [{ path: "/settings/pipelines", element: <PipelinesPage /> }];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.listPipelines.mockResolvedValue({ pipelines: [makePipeline()] });
  crm.listPipelineStages.mockResolvedValue({ stages: makeSalesStages() });
});

async function loaded() {
  const view = renderRoutes(routes, ["/settings/pipelines"]);
  // Wait for the stage list, not the heading — the loading state renders a
  // heading too, and scanning that would prove nothing.
  await screen.findByText("Prospecting");
  return view;
}

/** This is the densest screen in the app for the kinds of defect a markup
 * scan can see: a selectable list, an inline stage list with per-row
 * controls, an add-stage form, and three dialogs. */
describe("PipelinesPage accessibility", () => {
  it("has no violations on the loaded page", async () => {
    const { container } = await loaded();
    await expectNoA11yViolations(container);
  });

  // A dialog is where an unlabelled control or an unnamed region usually
  // hides, and axe only sees what is rendered — so each one is opened.
  it("has no violations with the stage editor open", async () => {
    const { container } = await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Edit stage Prospecting" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });

  it("has no violations with the archive confirmation open", async () => {
    const { container } = await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Archive stage Prospecting" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });

  it("has no violations for a reader who cannot write", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:pipeline:write");
    const { container } = await loaded();
    expect(screen.queryByRole("button", { name: /^Archive/ })).not.toBeInTheDocument();
    await expectNoA11yViolations(container);
  });

  // Archiving a pipeline used to go through window.confirm, which no test can
  // answer and which a browser may suppress into a silent false. These three
  // pin the whole path now that it is the app's own dialog.
  it("asks before archiving a pipeline, and does nothing until confirmed", async () => {
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Archive pipeline Sales" }));

    await screen.findByRole("dialog");
    expect(crm.updatePipeline).not.toHaveBeenCalled();
  });

  it("archives the pipeline once confirmed", async () => {
    crm.updatePipeline.mockResolvedValue({ pipeline: makePipeline({ archivedAt: makeTimestamp("2026-03-01T10:00:00Z") }) });
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Archive pipeline Sales" }));
    await screen.findByRole("dialog");

    fireEvent.click(screen.getByRole("button", { name: "Archive pipeline" }));

    await waitFor(() => expect(crm.updatePipeline).toHaveBeenCalledTimes(1));
    const sent = crm.updatePipeline.mock.calls[0][0].pipeline;
    expect(sent.archivedAt).toBeDefined();
    // A pipeline cannot be both archived and the default new deals land on.
    expect(sent.isDefault).toBe(false);
  });

  // The server refuses this while open deals stand on the pipeline, naming
  // them. That sentence is the point, so it must not be replaced by the
  // generic conflict line.
  it("shows the server's refusal verbatim", async () => {
    crm.updatePipeline.mockRejectedValue(
      new ConnectError('pipeline "Sales" still holds 2 open opportunities (Acme renewal, Contoso) — move them to another stage before archiving it', Code.Aborted),
    );
    await loaded();
    fireEvent.click(screen.getByRole("button", { name: "Archive pipeline Sales" }));
    await screen.findByRole("dialog");
    fireEvent.click(screen.getByRole("button", { name: "Archive pipeline" }));

    await screen.findByText(/still holds 2 open opportunities/);
  });

});

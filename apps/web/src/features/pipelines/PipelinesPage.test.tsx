import { fireEvent, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import PipelinesPage from "./PipelinesPage";
import { makePipeline, makeSalesStages, renderRoutes } from "../../test/harness";
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
    const stageRow = screen.getByText("Prospecting").closest("li")!;
    fireEvent.click(within(stageRow).getByRole("button", { name: "Edit" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });

  // The stage row's Archive, not the pipeline header's — the latter still
  // goes through window.confirm, which jsdom answers falsy so nothing opens.
  // Worth noting on its own: the two archive paths in this screen ask for
  // confirmation in two different ways.
  it("has no violations with the archive confirmation open", async () => {
    const { container } = await loaded();
    const stageRow = screen.getByText("Prospecting").closest("li")!;
    fireEvent.click(within(stageRow).getByRole("button", { name: "Archive" }));
    await screen.findByRole("dialog");
    await expectNoA11yViolations(container);
  });

  it("has no violations for a reader who cannot write", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:pipeline:write");
    const { container } = await loaded();
    expect(screen.queryByRole("button", { name: "Archive" })).not.toBeInTheDocument();
    await expectNoA11yViolations(container);
  });
});

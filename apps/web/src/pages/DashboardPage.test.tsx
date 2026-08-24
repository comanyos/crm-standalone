import { screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import DashboardPage from "./DashboardPage";
import { renderRoutes } from "../test/harness";
import { expectNoA11yViolations } from "../test/a11y";

const { crm } = vi.hoisted(() => ({
  crm: { getSummary: vi.fn(), getDashboardReport: vi.fn() },
}));

vi.mock("../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));

const routes = [{ path: "/", element: <DashboardPage /> }];

const report = {
  $typeName: "crm.v1.DashboardReport",
  pipelines: [
    {
      $typeName: "crm.v1.PipelineReport",
      pipelineId: "pipe-1", pipelineName: "Sales",
      stages: [
        { $typeName: "crm.v1.PipelineStageSummary", stageId: "stage-1", stageName: "Prospecting", terminalType: "open", count: 2, amount: 500 },
      ],
      wonCount: 1, wonAmount: 1000, lostCount: 0, lostAmount: 0,
    },
  ],
  tasks: {
    $typeName: "crm.v1.TaskSummary",
    openCount: 3, inProgressCount: 1, completedCount: 5, cancelledCount: 0, overdueCount: 2,
  },
  conversion: {
    $typeName: "crm.v1.ConversionFunnel",
    funnel: [
      { $typeName: "crm.v1.LeadFunnelStage", status: "new", count: 4 },
      { $typeName: "crm.v1.LeadFunnelStage", status: "working", count: 2 },
      { $typeName: "crm.v1.LeadFunnelStage", status: "qualified", count: 1 },
      { $typeName: "crm.v1.LeadFunnelStage", status: "converted", count: 1 },
      { $typeName: "crm.v1.LeadFunnelStage", status: "disqualified", count: 1 },
    ],
    totalLeads: 9, convertedLeads: 1, conversionRate: 1 / 9,
  },
  forecast: {
    $typeName: "crm.v1.ForecastSummary",
    byOwner: { "owner-1": 250 }, byStage: { prospecting: 250 }, total: 250,
    buckets: [
      { $typeName: "crm.v1.ForecastBucket", ownerId: "owner-1", stage: "prospecting", count: 2, amount: 2500, weighted: 250 },
    ],
  },
};

beforeEach(() => {
  vi.clearAllMocks();
  crm.getSummary.mockResolvedValue({ total: 12, counts: { account: 5, contact: 7 } });
  crm.getDashboardReport.mockResolvedValue({ report });
});

describe("DashboardPage", () => {
  it("renders pipeline, task, conversion and forecast sections", async () => {
    renderRoutes(routes, ["/"]);
    expect(await screen.findByText("Sales")).toBeInTheDocument();
    expect(screen.getByText("Prospecting")).toBeInTheDocument();
    expect(screen.getByText("2 · $500.00")).toBeInTheDocument();
    expect(screen.getByText("Overdue")).toBeInTheDocument();
    expect(screen.getByText("11.1%")).toBeInTheDocument();
    expect(screen.getByText(/conversion rate/)).toBeInTheDocument();
    expect(screen.getByText("Weighted forecast")).toBeInTheDocument();
  });

  it("shows an empty state when the tenant has no pipelines", async () => {
    crm.getDashboardReport.mockResolvedValue({ report: { ...report, pipelines: [] } });
    renderRoutes(routes, ["/"]);
    expect(await screen.findByText("No pipelines yet")).toBeInTheDocument();
  });

  it("has no accessibility violations once loaded", async () => {
    const { container } = renderRoutes(routes, ["/"]);
    await screen.findByText("Sales");
    await expectNoA11yViolations(container);
  });
});

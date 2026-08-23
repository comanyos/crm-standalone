import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CommandPalette } from "./CommandPalette";
import { renderRoutes } from "../../test/harness";

const { crm } = vi.hoisted(() => ({
  crm: {
    searchRecords: vi.fn(),
  },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));

const routes = [
  { path: "/", element: <CommandPalette /> },
  { path: "/accounts/:id", element: <p>Account detail</p> },
  { path: "/contacts/:id", element: <p>Contact detail</p> },
  { path: "/leads/:id", element: <p>Lead detail</p> },
  { path: "/opportunities/:id", element: <p>Opportunity detail</p> },
  { path: "/tasks/:id/edit", element: <p>Task detail</p> },
];

beforeEach(() => {
  vi.clearAllMocks();
  crm.searchRecords.mockResolvedValue({
    records: [
      { objectType: "account", id: "acc-1", title: "Acme Holdings", subtitle: "Software" },
      { objectType: "lead", id: "lead-1", title: "Acme Prospect", subtitle: "Referral" },
    ],
  });
});

describe("CommandPalette", () => {
  it("searches CRM records and opens the selected result", async () => {
    const { router } = renderRoutes(routes);
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    fireEvent.change(screen.getByLabelText("Search records or jump to a section"), {
      target: { value: "Acme" },
    });

    expect(await screen.findByText("Acme Holdings")).toBeInTheDocument();
    expect(screen.getByText("Acme Prospect")).toBeInTheDocument();
    expect(crm.searchRecords).toHaveBeenCalledWith({ query: "Acme", pageSize: 20 });

    fireEvent.click(screen.getByText("Acme Holdings"));
    await waitFor(() => expect(router.state.location.pathname).toBe("/accounts/acc-1"));
  });
});

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import LeadFormPage from "./LeadFormPage";
import { makeLead, makeTimestamp, renderRoutes, sampleCustomFields } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm, auth } = vi.hoisted(() => ({
  crm: { getLead: vi.fn(), createLead: vi.fn(), updateLead: vi.fn() },
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
  { path: "/leads", element: <p>Leads list</p> },
  { path: "/leads/:id", element: <p>Lead detail</p> },
  { path: "/leads/:id/edit", element: <LeadFormPage mode="edit" /> },
];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  auth.hasRole.mockReturnValue(true);
  crm.getLead.mockResolvedValue({ lead: makeLead({ updatedAt: savedAt }) });
  crm.updateLead.mockResolvedValue({ lead: makeLead() });
});

describe("LeadFormPage", () => {
  // The two fields the form has no input for and would drop by omission.
  // updated_at is the version the edit was opened on; custom_fields is a
  // document UpdateLead replaces rather than merges, so a save without it
  // deletes every custom value on the lead — and fails outright where the
  // tenant has marked one required.
  it("round-trips the fields it does not expose", async () => {
    renderRoutes(routes, ["/leads/lead-1/edit"]);
    await waitFor(() => expect(screen.getByLabelText(/^Company/)).toHaveValue("Northwind"));

    fireEvent.change(screen.getByLabelText(/^Company/), { target: { value: "Northwind Traders" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(crm.updateLead).toHaveBeenCalledTimes(1));
    const sent = crm.updateLead.mock.calls[0][0].lead;
    expect(sent).toMatchObject({ id: "lead-1", company: "Northwind Traders" });
    expect(sent.updatedAt).toEqual(savedAt);
    expect(sent.customFields).toEqual(sampleCustomFields);
  });

  // A form is where label association and error-to-field wiring live, and
  // both are invisible until something reads the page aloud.
  it("has no accessibility violations", async () => {
    const { container } = renderRoutes(routes, ["/leads/lead-1/edit"]);
    await waitFor(() => expect(screen.getByLabelText(/^Company/)).toHaveValue("Northwind"));
    await expectNoA11yViolations(container);
  });

});

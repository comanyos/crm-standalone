import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MergeAccountsDialog } from "./MergeAccountsDialog";
import { makeAccount, renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm } = vi.hoisted(() => ({
  crm: { getAccount: vi.fn(), mergeAccounts: vi.fn() },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));

const onClose = vi.fn();
const onMerged = vi.fn();

function renderDialog(open = true) {
  const routes = [{
    path: "/",
    element: (
      <MergeAccountsDialog open={open} onClose={onClose} survivorId="acc-1" duplicateId="acc-2" onMerged={onMerged} />
    ),
  }];
  return renderRoutes(routes);
}

beforeEach(() => {
  vi.clearAllMocks();
  crm.getAccount.mockImplementation(({ id }: { id: string }) =>
    Promise.resolve({
      account: id === "acc-2"
        ? makeAccount({ id: "acc-2", name: "Acme Incorporated", tier: "gold", industry: "Software" })
        : makeAccount({ id: "acc-1", name: "Acme Corp", tier: "silver", industry: "Software" }),
    }),
  );
  crm.mergeAccounts.mockResolvedValue({ account: makeAccount(), merge: {} });
});

describe("MergeAccountsDialog", () => {
  it("defaults every differing field to the survivor's own value", async () => {
    renderDialog();
    await screen.findByText("will be kept", { exact: false });

    fireEvent.click(screen.getByRole("button", { name: "Merge — cannot be undone" }));

    await waitFor(() => expect(crm.mergeAccounts).toHaveBeenCalledWith({
      survivorId: "acc-1", duplicateId: "acc-2", fieldResolutions: {},
    }));
  });

  it("sends only the fields explicitly switched to the duplicate's value", async () => {
    renderDialog();
    await screen.findByText("will be kept", { exact: false });

    // Tier differs (silver vs gold); switch it to the duplicate's value.
    const tierRow = screen.getByText("Tier").closest("tr")!;
    fireEvent.click(within(tierRow).getByText("gold"));

    fireEvent.click(screen.getByRole("button", { name: "Merge — cannot be undone" }));

    await waitFor(() => expect(crm.mergeAccounts).toHaveBeenCalledWith({
      survivorId: "acc-1", duplicateId: "acc-2", fieldResolutions: { tier: "duplicate" },
    }));
  });

  // The server's field allowlist is snake_case (owner_id, annual_revenue,
  // parent_id); the TS client's own Account properties are camelCase. This
  // pins the translation — sending the camelCase key is a request the
  // server correctly rejects.
  it("translates camelCase field keys to the server's snake_case allowlist", async () => {
    crm.getAccount.mockImplementation(({ id }: { id: string }) =>
      Promise.resolve({
        account: id === "acc-2"
          ? makeAccount({ id: "acc-2", name: "Acme Incorporated", ownerId: "bob" })
          : makeAccount({ id: "acc-1", name: "Acme Corp", ownerId: "alice" }),
      }),
    );
    renderDialog();
    await screen.findByText("will be kept", { exact: false });

    const ownerRow = screen.getByText("Owner").closest("tr")!;
    fireEvent.click(within(ownerRow).getByText("bob"));

    fireEvent.click(screen.getByRole("button", { name: "Merge — cannot be undone" }));

    await waitFor(() => expect(crm.mergeAccounts).toHaveBeenCalledWith({
      survivorId: "acc-1", duplicateId: "acc-2", fieldResolutions: { owner_id: "duplicate" },
    }));
  });

  it("does not offer a choice for fields that already match", async () => {
    crm.getAccount.mockImplementation(({ id }: { id: string }) =>
      Promise.resolve({ account: makeAccount({ id, name: "Acme Corp", tier: "gold" }) }),
    );
    renderDialog();
    await screen.findByText("will be kept", { exact: false });

    const nameRow = screen.getByText("Name").closest("tr")!;
    // Only one radio (the survivor's own, disabled) when both sides agree —
    // there is nothing to resolve.
    expect(within(nameRow).getAllByRole("radio")).toHaveLength(1);
    expect(within(nameRow).getByRole("radio")).toBeDisabled();
  });

  it("calls onMerged, not onClose directly, when the merge succeeds", async () => {
    renderDialog();
    await screen.findByText("will be kept", { exact: false });

    fireEvent.click(screen.getByRole("button", { name: "Merge — cannot be undone" }));

    await waitFor(() => expect(onMerged).toHaveBeenCalled());
    expect(onClose).not.toHaveBeenCalled();
  });

  it("has no accessibility violations", async () => {
    const { container } = renderDialog();
    await screen.findByText("will be kept", { exact: false });
    await expectNoA11yViolations(container);
  });
});

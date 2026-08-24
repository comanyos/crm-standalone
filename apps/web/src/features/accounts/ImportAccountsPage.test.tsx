import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ImportAccountsPage from "./ImportAccountsPage";
import { renderRoutes } from "../../test/harness";
import { expectNoA11yViolations } from "../../test/a11y";

const { crm } = vi.hoisted(() => ({
  crm: { createImportJob: vi.fn(), commitImportJob: vi.fn() },
}));

vi.mock("../../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));

const routes = [
  { path: "/accounts", element: <p>Accounts list</p> },
  { path: "/accounts/import", element: <ImportAccountsPage /> },
];

const csvContent = "Company,Owner\nAcme,alice\nUmbrella,bob\n";

function makeFile(content: string, name = "accounts.csv") {
  return new File([content], name, { type: "text/csv" });
}

beforeEach(() => {
  vi.clearAllMocks();
  crm.createImportJob.mockResolvedValue({
    job: {
      $typeName: "crm.v1.ImportJob", id: "job-1", objectType: "account", status: "previewed",
      fileName: "accounts.csv", mapping: { Company: "name" }, rowCount: 2, createdRows: 0,
      skippedRows: 0, errors: [], createdBy: "user-1",
    },
  });
  crm.commitImportJob.mockResolvedValue({
    job: {
      $typeName: "crm.v1.ImportJob", id: "job-1", objectType: "account", status: "completed",
      fileName: "accounts.csv", mapping: { Company: "name" }, rowCount: 2, createdRows: 2,
      skippedRows: 0, errors: [], createdBy: "user-1",
    },
  });
});

async function chooseFile(content = csvContent) {
  renderRoutes(routes, ["/accounts/import"]);
  const input = document.querySelector('input[type="file"]') as HTMLInputElement;
  fireEvent.change(input, { target: { files: [makeFile(content)] } });
  await screen.findByText(/map each column/i);
}

describe("ImportAccountsPage", () => {
  it("shows a mapping control for every CSV column after a file is chosen", async () => {
    await chooseFile();
    expect(screen.getByLabelText("Company")).toBeInTheDocument();
    expect(screen.getByLabelText("Owner")).toBeInTheDocument();
  });

  it("requires a name mapping before preview is allowed", async () => {
    await chooseFile();
    expect(screen.getByRole("button", { name: "Preview import" })).toBeDisabled();

    fireEvent.change(screen.getByLabelText("Company"), { target: { value: "name" } });
    expect(screen.getByRole("button", { name: "Preview import" })).toBeEnabled();
  });

  it("previews without committing, then commits on request", async () => {
    await chooseFile();
    fireEvent.change(screen.getByLabelText("Company"), { target: { value: "name" } });
    fireEvent.change(screen.getByLabelText("Owner"), { target: { value: "owner_id" } });

    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));

    await waitFor(() => expect(crm.createImportJob).toHaveBeenCalledWith({
      objectType: "account", fileName: "accounts.csv", content: csvContent,
      mapping: { Company: "name", Owner: "owner_id" },
    }));
    expect(crm.commitImportJob).not.toHaveBeenCalled();
    await screen.findByRole("button", { name: "Commit import — creates 2 account(s)" });

    fireEvent.click(screen.getByRole("button", { name: /Commit import/ }));
    await waitFor(() => expect(crm.commitImportJob).toHaveBeenCalledWith({ id: "job-1" }));
    expect(await screen.findByText("2 created")).toBeInTheDocument();
  });

  it("shows row-level errors from the preview without blocking the whole batch", async () => {
    crm.createImportJob.mockResolvedValue({
      job: {
        $typeName: "crm.v1.ImportJob", id: "job-2", objectType: "account", status: "previewed",
        fileName: "accounts.csv", mapping: { Company: "name" }, rowCount: 2, createdRows: 0,
        skippedRows: 0, errors: [{ $typeName: "crm.v1.ImportRowError", rowNumber: 2, message: "name is required" }],
        createdBy: "user-1",
      },
    });
    await chooseFile();
    fireEvent.change(screen.getByLabelText("Company"), { target: { value: "name" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview import" }));

    expect(await screen.findByText("name is required")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Commit import — creates 1 account/ })).toBeInTheDocument();
  });

  it("has no accessibility violations at each stage", async () => {
    const { container } = await (async () => {
      const view = renderRoutes(routes, ["/accounts/import"]);
      await screen.findByText("Choose file");
      return view;
    })();
    await expectNoA11yViolations(container);

    const input = document.querySelector('input[type="file"]') as HTMLInputElement;
    fireEvent.change(input, { target: { files: [makeFile(csvContent)] } });
    await screen.findByText(/map each column/i);
    await expectNoA11yViolations(container);
  });
});

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { NotesPanel } from "./NotesPanel";
import { makeNote, makeTimestamp, renderRoutes } from "../test/harness";

const { crm, auth } = vi.hoisted(() => ({
  crm: { listNotes: vi.fn(), createNote: vi.fn(), updateNote: vi.fn(), deleteNote: vi.fn() },
  auth: {
    hasScope: vi.fn(),
    hasRole: vi.fn(),
    status: "authenticated",
    session: { claims: { sub: "user-1" } },
    login: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("../lib/useCrmClients", () => ({ useCrmClients: () => ({ crm, module: {} }) }));
vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));

const routes = [{ path: "/", element: <NotesPanel subjectType="account" subjectId="acc-1" /> }];

beforeEach(() => {
  vi.clearAllMocks();
  auth.hasScope.mockReturnValue(true);
  crm.listNotes.mockResolvedValue({ notes: [makeNote()], nextPageToken: "" });
  crm.createNote.mockResolvedValue({ note: makeNote() });
  crm.updateNote.mockResolvedValue({ note: makeNote() });
  crm.deleteNote.mockResolvedValue({});
});

describe("NotesPanel", () => {
  it("lists the record's notes", async () => {
    renderRoutes(routes);
    expect(await screen.findByText("Renewal owner is now Priya.")).toBeInTheDocument();
    expect(crm.listNotes).toHaveBeenCalledWith(
      expect.objectContaining({ subjectType: "account", subjectId: "acc-1" }),
    );
  });

  it("adds a note against the record it is mounted on", async () => {
    renderRoutes(routes);
    await screen.findByText("Renewal owner is now Priya.");

    fireEvent.click(screen.getByRole("button", { name: "Add note" }));
    fireEvent.change(screen.getByLabelText("Note"), { target: { value: "  Spoke to procurement.  " } });
    fireEvent.click(screen.getByRole("button", { name: "Save note" }));

    await waitFor(() =>
      expect(crm.createNote).toHaveBeenCalledWith({
        note: { body: "Spoke to procurement.", subjectType: "account", subjectId: "acc-1" },
      }),
    );
  });

  // updatedAt is the version the edit was opened on. Dropping it would let
  // two people editing the same note silently overwrite each other instead
  // of one being told there was a conflict.
  it("round-trips the note's version when editing", async () => {
    const note = makeNote({ updatedAt: makeTimestamp("2026-08-22T10:00:00Z") });
    crm.listNotes.mockResolvedValue({ notes: [note], nextPageToken: "" });
    renderRoutes(routes);
    await screen.findByText("Renewal owner is now Priya.");

    fireEvent.click(screen.getByRole("button", { name: "Edit note" }));
    fireEvent.change(screen.getByLabelText("Note"), { target: { value: "Corrected." } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() =>
      expect(crm.updateNote).toHaveBeenCalledWith({
        note: { id: "note-1", body: "Corrected.", updatedAt: note.updatedAt },
      }),
    );
  });

  // A note is mutable, so a reader needs to be able to tell a corrected one
  // from an original without diffing anything.
  it("shows when a note has been edited, and does not when it has not", async () => {
    crm.listNotes.mockResolvedValue({ notes: [makeNote()], nextPageToken: "" });
    const { rerender } = renderRoutes(routes);
    await screen.findByText("Renewal owner is now Priya.");
    expect(screen.queryByText(/edited/i)).not.toBeInTheDocument();
    rerender(<div />);

    crm.listNotes.mockResolvedValue({
      notes: [makeNote({ editedAt: makeTimestamp("2026-08-22T11:00:00Z") })],
      nextPageToken: "",
    });
    renderRoutes(routes);
    expect(await screen.findByText(/edited/i)).toBeInTheDocument();
  });

  it("confirms before deleting", async () => {
    renderRoutes(routes);
    await screen.findByText("Renewal owner is now Priya.");

    fireEvent.click(screen.getByRole("button", { name: "Delete note" }));
    expect(crm.deleteNote).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("dialog").querySelector("button.bg-rose-600") ?? screen.getAllByRole("button", { name: "Delete" })[0]);
    await waitFor(() => expect(crm.deleteNote).toHaveBeenCalledWith({ id: "note-1" }));
  });

  it("offers no write actions without the note write scope", async () => {
    auth.hasScope.mockImplementation((scope: string) => scope !== "crm:note:write");
    renderRoutes(routes);
    await screen.findByText("Renewal owner is now Priya.");

    expect(screen.queryByRole("button", { name: "Add note" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit note" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete note" })).not.toBeInTheDocument();
  });
});

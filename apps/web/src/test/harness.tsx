import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryRouter, type RouteObject } from "react-router-dom";
import { ToastProvider } from "../components/ui/Toast";
import { timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import type { Account, Contact, Note, Task } from "@crm/contracts";

/** Renders a route tree on a memory **data** router.
 *
 * A data router (not <MemoryRouter>) is deliberate: the account/contact
 * forms guard unsaved changes with `useBlocker`, which only exists on a data
 * router — testing them through <MemoryRouter> would silently exercise a
 * different code path than production (see App.tsx's router).
 *
 * Retries are off so a test asserting an error state doesn't wait through
 * react-query's backoff. */
export function renderRoutes(routes: RouteObject[], initialEntries: string[] = ["/"]) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const router = createMemoryRouter(routes, { initialEntries });
  const view = render(
    <ToastProvider>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ToastProvider>,
  );
  return { ...view, router };
}

/** A fully-populated Account, so a test only states the fields it cares
 * about. Protobuf-es v2 message types are plain objects plus $typeName, so
 * a literal is a faithful stand-in for a decoded response. */
export function makeAccount(overrides: Partial<Account> = {}): Account {
  return {
    $typeName: "crm.v1.Account",
    id: "acc-1",
    name: "Acme Corp",
    industry: "Software",
    tier: "gold",
    ownerId: "user-1",
    website: "https://acme.example",
    annualRevenue: 0,
    parentId: "",
    duplicateOfAccountId: "",
    ...overrides,
  } as Account;
}

export function makeContact(overrides: Partial<Contact> = {}): Contact {
  return {
    $typeName: "crm.v1.Contact",
    id: "con-1",
    email: "ada@acme.example",
    accountId: "acc-1",
    firstName: "Ada",
    lastName: "Lovelace",
    phone: "",
    title: "",
    role: "",
    duplicateOfContactId: "",
    ...overrides,
  } as Contact;
}

/** A fully-populated Task. Defaults to an open, unattached, undated task —
 * the shape a test has to opt out of rather than into, since "no due date"
 * and "no subject" are real states rather than incomplete ones. */
export function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    $typeName: "crm.v1.Task",
    id: "task-1",
    title: "Send the proposal",
    description: "",
    status: "open",
    priority: "normal",
    assigneeId: "user-1",
    dueAt: undefined,
    subjectType: "",
    subjectId: "",
    completedAt: undefined,
    completedBy: "",
    createdBy: "user-1",
    ...overrides,
  } as Task;
}

/** A fully-populated Note. editedAt is undefined by default: a new note has
 * not been edited, and the panel renders that difference. */
export function makeNote(overrides: Partial<Note> = {}): Note {
  return {
    $typeName: "crm.v1.Note",
    id: "note-1",
    body: "Renewal owner is now Priya.",
    subjectType: "account",
    subjectId: "acc-1",
    authorId: "user-1",
    editedAt: undefined,
    ...overrides,
  } as Note;
}

/** A protobuf Timestamp built the way the wire decoder builds one, for
 * tests that assert a record's updated_at is round-tripped verbatim (the
 * optimistic-concurrency check in UpdateAccount/UpdateContact depends on
 * exactly that value coming back). */
export function makeTimestamp(iso: string): Timestamp {
  return timestampFromDate(new Date(iso));
}

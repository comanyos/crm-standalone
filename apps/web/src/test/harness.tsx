import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryRouter, type RouteObject } from "react-router-dom";
import { ToastProvider } from "../components/ui/Toast";
import { timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import type { Account, Contact, CustomFieldDefinition, CustomObjectDefinition, Lead, Note, Opportunity, Pipeline, PipelineStage, Task } from "@crm/contracts";

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

/** A sample custom-field document. Every factory below carries one, because
 * the write paths replace this document rather than merging it: a form that
 * forgets to round-trip it deletes the tenant's data, and a test whose
 * fixture has no custom values cannot see that happen. */
export const sampleCustomFields = {
  deal_source: { $typeName: "crm.v1.CustomValue" as const, kind: { case: "textValue" as const, value: "tradeshow" } },
};

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
    customFields: sampleCustomFields,
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
    customFields: sampleCustomFields,
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
    customFields: sampleCustomFields,
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
    customFields: sampleCustomFields,
    ...overrides,
  } as Note;
}

/** A fully-populated Lead in the default `new` status. */
export function makeLead(overrides: Partial<Lead> = {}): Lead {
  return {
    $typeName: "crm.v1.Lead",
    id: "lead-1",
    company: "Northwind",
    contactEmail: "buyer@northwind.example",
    source: "Webinar",
    ownerId: "user-1",
    score: 20,
    status: "new",
    disqualifiedReason: "",
    duplicateOfContactId: "",
    convertedContactId: "",
    convertedAccountId: "",
    convertedOpportunityId: "",
    customFields: sampleCustomFields,
    ...overrides,
  } as Lead;
}

/** A protobuf Timestamp built the way the wire decoder builds one, for
 * tests that assert a record's updated_at is round-tripped verbatim (the
 * optimistic-concurrency check in UpdateAccount/UpdateContact depends on
 * exactly that value coming back). */
export function makeTimestamp(iso: string): Timestamp {
  return timestampFromDate(new Date(iso));
}

/** The system Sales pipeline's stages, the shape the backend provisions for a
 * tenant that has configured none of its own (systemSalesStages in
 * opportunity_pipeline_config.go): four open stages in position order, then a
 * won and a lost stage that both require a close reason. */
export function makeSalesStages(): PipelineStage[] {
  const stage = (id: string, name: string, position: number, overrides: Partial<PipelineStage> = {}) =>
    ({
      $typeName: "crm.v1.PipelineStage",
      id,
      pipelineId: "system-sales",
      name,
      position,
      probability: 0,
      terminalType: "open",
      requiresCloseReason: false,
      ...overrides,
    }) as PipelineStage;
  return [
    stage("prospecting", "Prospecting", 0),
    stage("qualification", "Qualification", 1),
    stage("proposal", "Proposal", 2),
    stage("negotiation", "Negotiation", 3),
    stage("closed_won", "Closed won", 4, { terminalType: "won", requiresCloseReason: true }),
    stage("closed_lost", "Closed lost", 5, { terminalType: "lost", requiresCloseReason: true }),
  ];
}

/** A fully-populated Opportunity on the default pipeline's first open stage.
 * customFields is populated by default so a test can catch a write path that
 * drops it — UpdateOpportunity replaces the document rather than merging it. */
export function makeOpportunity(overrides: Partial<Opportunity> = {}): Opportunity {
  return {
    $typeName: "crm.v1.Opportunity",
    id: "opp-1",
    name: "Acme renewal",
    accountId: "acc-1",
    stage: "prospecting",
    pipelineId: "system-sales",
    stageId: "prospecting",
    amount: 50000,
    probability: 10,
    closeDate: undefined,
    closeReason: "",
    customFields: sampleCustomFields,
    ...overrides,
  } as Opportunity;
}

/** The tenant's default Sales pipeline, as ListPipelines returns it. */
export function makePipeline(overrides: Partial<Pipeline> = {}): Pipeline {
  return {
    $typeName: "crm.v1.Pipeline",
    id: "system-sales",
    name: "Sales",
    isDefault: true,
    ...overrides,
  } as Pipeline;
}

export function makeCustomField(overrides: Partial<CustomFieldDefinition> = {}): CustomFieldDefinition {
  return {
    $typeName: "crm.v1.CustomFieldDefinition",
    id: "cf-1",
    objectType: "account",
    fieldKey: "deal_source",
    label: "Deal source",
    dataType: "text",
    required: false,
    ...overrides,
  } as CustomFieldDefinition;
}

export function makeCustomObject(overrides: Partial<CustomObjectDefinition> = {}): CustomObjectDefinition {
  return {
    $typeName: "crm.v1.CustomObjectDefinition",
    id: "co-1",
    apiName: "shipment",
    label: "Shipment",
    pluralLabel: "Shipments",
    ...overrides,
  } as CustomObjectDefinition;
}

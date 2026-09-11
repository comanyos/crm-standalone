import { createBrowserRouter, createRoutesFromElements, Route, Outlet } from "react-router-dom";
import { lazy } from "react";
import { AppShell } from "./components/shell/AppShell";
import { RequireAuth } from "./auth/RequireAuth";
import { ErrorBoundary } from "./components/ErrorBoundary";
import LoginPage from "./pages/LoginPage";
import SignedOutPage from "./pages/SignedOutPage";
import NotFoundPage from "./pages/NotFoundPage";

// Operational screens are route-split so a rep opening the dashboard does
// not download every form, board, integration and administration workflow.
// AppShell supplies one consistent loading state while a route chunk arrives.
const AdminPage = lazy(() => import("./pages/AdminPage"));
const HomePage = lazy(() => import("./pages/HomePage"));
const AccountsListPage = lazy(() => import("./features/accounts/AccountsListPage"));
const AccountDetailPage = lazy(() => import("./features/accounts/AccountDetailPage"));
const AccountFormPage = lazy(() => import("./features/accounts/AccountFormPage"));
const ImportAccountsPage = lazy(() => import("./features/accounts/ImportAccountsPage"));
const ContactsListPage = lazy(() => import("./features/contacts/ContactsListPage"));
const ContactDetailPage = lazy(() => import("./features/contacts/ContactDetailPage"));
const ContactFormPage = lazy(() => import("./features/contacts/ContactFormPage"));
const LeadsListPage = lazy(() => import("./features/leads/LeadsListPage"));
const LeadDetailPage = lazy(() => import("./features/leads/LeadDetailPage"));
const LeadFormPage = lazy(() => import("./features/leads/LeadFormPage"));
const OpportunitiesListPage = lazy(() => import("./features/opportunities/OpportunitiesListPage"));
const OpportunityDetailPage = lazy(() => import("./features/opportunities/OpportunityDetailPage"));
const OpportunityFormPage = lazy(() => import("./features/opportunities/OpportunityFormPage"));
const OpportunityBoardPage = lazy(() => import("./features/opportunities/OpportunityBoardPage"));
const PipelinesPage = lazy(() => import("./features/pipelines/PipelinesPage"));
const TasksListPage = lazy(() => import("./features/tasks/TasksListPage"));
const TaskFormPage = lazy(() => import("./features/tasks/TaskFormPage"));
const CalendarPage = lazy(() => import("./features/calendar/CalendarPage"));
const CustomFieldsPage = lazy(() => import("./features/settings/CustomFieldsPage"));
const CustomObjectsPage = lazy(() => import("./features/settings/CustomObjectsPage"));
const TagsPage = lazy(() => import("./features/settings/TagsPage"));
const MessageTemplatesPage = lazy(() => import("./features/settings/MessageTemplatesPage"));
const LeadCapturePage = lazy(() => import("./features/settings/LeadCapturePage"));
const IntegrationsPage = lazy(() => import("./features/integrations/IntegrationsPage"));
const AutomationPage = lazy(() => import("./features/automation/AutomationPage"));

/** A data router (rather than plain <BrowserRouter>) is required for
 * useBlocker, which the account/contact forms use for unsaved-change
 * protection (task.md Phase 3.2). The route tree itself is unchanged from
 * the previous <Routes> version — only the top-level ErrorBoundary wrapper
 * moved to its own pathless layout route, since RouterProvider renders the
 * matched route element directly rather than a single <App/> component. */
export const router = createBrowserRouter(
  createRoutesFromElements(
    <Route
      element={
        <ErrorBoundary>
          <Outlet />
        </ErrorBoundary>
      }
    >
      <Route path="/login" element={<LoginPage />} />
      <Route path="/signed-out" element={<SignedOutPage />} />

      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route index element={<HomePage />} />
        <Route path="/admin" element={<AdminPage />} />

        <Route path="/accounts" element={<AccountsListPage />} />
        <Route path="/accounts/new" element={<AccountFormPage mode="create" />} />
        <Route path="/accounts/import" element={<ImportAccountsPage />} />
        <Route path="/accounts/:id" element={<AccountDetailPage />} />
        <Route path="/accounts/:id/edit" element={<AccountFormPage mode="edit" />} />

        <Route path="/contacts" element={<ContactsListPage />} />
        <Route path="/contacts/new" element={<ContactFormPage mode="create" />} />
        <Route path="/contacts/:id" element={<ContactDetailPage />} />
        <Route path="/contacts/:id/edit" element={<ContactFormPage mode="edit" />} />

        <Route path="/tasks" element={<TasksListPage />} />
        <Route path="/calendar" element={<CalendarPage />} />
        <Route path="/tasks/new" element={<TaskFormPage mode="create" />} />
        <Route path="/tasks/:id/edit" element={<TaskFormPage mode="edit" />} />

        <Route path="/leads" element={<LeadsListPage />} />
        <Route path="/leads/new" element={<LeadFormPage mode="create" />} />
        <Route path="/leads/:id" element={<LeadDetailPage />} />
        <Route path="/leads/:id/edit" element={<LeadFormPage mode="edit" />} />
        <Route path="/opportunities" element={<OpportunitiesListPage />} />
        <Route path="/opportunities/board" element={<OpportunityBoardPage />} />
        <Route path="/opportunities/new" element={<OpportunityFormPage mode="create" />} />
        <Route path="/opportunities/:id" element={<OpportunityDetailPage />} />
        <Route path="/opportunities/:id/edit" element={<OpportunityFormPage mode="edit" />} />
        <Route path="/settings/automation" element={<AutomationPage />} />
        <Route path="/settings/pipelines" element={<PipelinesPage />} />
        <Route path="/settings/custom-fields" element={<CustomFieldsPage />} />
        <Route path="/settings/custom-objects" element={<CustomObjectsPage />} />
        <Route path="/settings/tags" element={<TagsPage />} />
        <Route path="/settings/message-templates" element={<MessageTemplatesPage />} />
        <Route path="/settings/lead-capture" element={<LeadCapturePage />} />
        <Route path="/settings/integrations" element={<IntegrationsPage />} />
      </Route>

      <Route path="*" element={<NotFoundPage />} />
    </Route>,
  ),
);

import { createBrowserRouter, createRoutesFromElements, Route, Outlet } from "react-router-dom";
import { AppShell } from "./components/shell/AppShell";
import { RequireAuth } from "./auth/RequireAuth";
import { ErrorBoundary } from "./components/ErrorBoundary";
import LoginPage from "./pages/LoginPage";
import CallbackPage from "./pages/CallbackPage";
import SignedOutPage from "./pages/SignedOutPage";
import DashboardPage from "./pages/DashboardPage";
import NotFoundPage from "./pages/NotFoundPage";
import AccountsListPage from "./features/accounts/AccountsListPage";
import AccountDetailPage from "./features/accounts/AccountDetailPage";
import AccountFormPage from "./features/accounts/AccountFormPage";
import ContactsListPage from "./features/contacts/ContactsListPage";
import ContactDetailPage from "./features/contacts/ContactDetailPage";
import ContactFormPage from "./features/contacts/ContactFormPage";
import LeadsListPage from "./features/leads/LeadsListPage";
import LeadDetailPage from "./features/leads/LeadDetailPage";
import LeadFormPage from "./features/leads/LeadFormPage";
import OpportunitiesListPage from "./features/opportunities/OpportunitiesListPage";
import OpportunityDetailPage from "./features/opportunities/OpportunityDetailPage";
import OpportunityFormPage from "./features/opportunities/OpportunityFormPage";
import OpportunityBoardPage from "./features/opportunities/OpportunityBoardPage";
import PipelinesPage from "./features/pipelines/PipelinesPage";
import TasksListPage from "./features/tasks/TasksListPage";
import TaskFormPage from "./features/tasks/TaskFormPage";
import CalendarPage from "./features/calendar/CalendarPage";
import CustomFieldsPage from "./features/settings/CustomFieldsPage";
import CustomObjectsPage from "./features/settings/CustomObjectsPage";
import IntegrationsPage from "./features/integrations/IntegrationsPage";
import AutomationPage from "./features/automation/AutomationPage";

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
      <Route path="/auth/callback" element={<CallbackPage />} />
      <Route path="/signed-out" element={<SignedOutPage />} />

      <Route
        element={
          <RequireAuth>
            <AppShell />
          </RequireAuth>
        }
      >
        <Route index element={<DashboardPage />} />

        <Route path="/accounts" element={<AccountsListPage />} />
        <Route path="/accounts/new" element={<AccountFormPage mode="create" />} />
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
        <Route path="/settings/integrations" element={<IntegrationsPage />} />
      </Route>

      <Route path="*" element={<NotFoundPage />} />
    </Route>,
  ),
);

import { BrowserRouter, Routes, Route } from 'react-router-dom'
import Layout from './components/Layout'
import Dashboard from './pages/Dashboard'
import Identities from './pages/Identities'
import IdentityDetail from './pages/IdentityDetail'
import Orgs from './pages/Orgs'
import Invitations from './pages/Invitations'
import Settings from './pages/Settings'
import Login from './pages/Login'
import AuditExport from './pages/AuditExport'
import SSOEnforcement from './pages/SSOEnforcement'
import Branding from './pages/Branding'
import Providers from './pages/Providers'
import Webhooks from './pages/Webhooks'
import Blueprints from './pages/Blueprints'
import Roles from './pages/Roles'
import Applications from './pages/Applications'
import Authorization from './pages/FGA'
import Verification from './pages/Verification'
import SelfServiceSettings from './pages/SelfServiceSettings'
import InvitationAccept from './pages/InvitationAccept'

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="login" element={<Login />} />
        <Route path="verification" element={<Verification />} />
        <Route path="account/settings" element={<SelfServiceSettings />} />
        <Route path="invitations/:token/accept" element={<InvitationAccept />} />
        <Route element={<Layout />}>
          <Route index element={<Dashboard />} />
          <Route path="identities" element={<Identities />} />
          <Route path="identities/:id" element={<IdentityDetail />} />
          <Route path="orgs" element={<Orgs />} />
          <Route path="invitations" element={<Invitations />} />
          <Route path="settings" element={<Settings />} />
          <Route path="enterprise/audit" element={<AuditExport />} />
          <Route path="enterprise/sso" element={<SSOEnforcement />} />
          <Route path="enterprise/branding" element={<Branding />} />
          <Route path="providers" element={<Providers />} />
          <Route path="webhooks" element={<Webhooks />} />
          <Route path="blueprints" element={<Blueprints />} />
          <Route path="roles" element={<Roles />} />
          <Route path="applications" element={<Applications />} />
          <Route path="authorization" element={<Authorization />} />
        </Route>
      </Routes>
    </BrowserRouter>
  )
}

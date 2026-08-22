import { NavLink, Outlet, useNavigate } from 'react-router-dom'

interface NavItem {
  to: string
  label: string
  enterprise?: boolean // only reachable when the server was built with the enterprise tag
}

const nav: NavItem[] = [
  { to: '/',            label: 'Dashboard' },
  { to: '/identities',  label: 'Identities' },
  { to: '/orgs',        label: 'Orgs' },
  { to: '/invitations', label: 'Invitations' },
  { to: '/settings',    label: 'Settings' },
  { to: '/providers',   label: 'Providers' },
  { to: '/webhooks',    label: 'Webhooks' },
  { to: '/blueprints',  label: 'Blueprints' },
  { to: '/roles',       label: 'Roles' },
  { to: '/applications', label: 'Applications' },
  { to: '/authorization', label: 'Authorization' },
  // Enterprise (compiled in only with the `enterprise` server build tag)
  { to: '/enterprise/audit',    label: 'Audit Export',    enterprise: true },
  { to: '/enterprise/sso',      label: 'SSO Enforcement', enterprise: true },
  { to: '/enterprise/branding', label: 'Branding',        enterprise: true },
]

export default function Layout() {
  const navigate = useNavigate()

  const logout = async () => {
    await fetch('/api/v1/sessions/me', { method: 'DELETE', credentials: 'include' }).catch(() => {})
    navigate('/login')
  }

  return (
    <div className="flex h-screen bg-gray-50">
      <aside className="w-56 bg-white border-r border-gray-200 flex flex-col">
        <div className="px-5 py-4 border-b border-gray-200">
          <span className="text-lg font-semibold text-brand-600">AuthinCore</span>
          <span className="ml-1 text-xs text-gray-400">Admin</span>
        </div>

        <nav className="flex-1 px-3 py-4 space-y-0.5 overflow-y-auto">
          {/* Core nav — always shown */}
          {nav.filter(n => !n.enterprise).map(({ to, label }) => (
            <NavLink
              key={to} to={to} end={to === '/'}
              className={({ isActive }) =>
                `flex items-center px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                  isActive ? 'bg-brand-50 text-brand-700' : 'text-gray-600 hover:bg-gray-100 hover:text-gray-900'
                }`
              }
            >
              {label}
            </NavLink>
          ))}

          {/* Enterprise section — routes exist only when the server was built
              with the `enterprise` tag; hitting one otherwise 404s. */}
          <div className="pt-4">
            <p className="px-3 pb-1 text-xs font-semibold text-gray-400 uppercase tracking-wider">
              Enterprise
            </p>
            {nav.filter(n => n.enterprise).map(({ to, label }) => (
              <NavLink
                key={to} to={to}
                className={({ isActive }) =>
                  `flex items-center justify-between px-3 py-2 rounded-md text-sm font-medium transition-colors ${
                    isActive ? 'bg-brand-50 text-brand-700' : 'text-gray-600 hover:bg-gray-100 hover:text-gray-900'
                  }`
                }
              >
                {label}
              </NavLink>
            ))}
          </div>

          {/* Logout */}
          <div className="pt-2 border-t border-gray-100 mt-2">
            <button
              onClick={logout}
              className="w-full flex items-center px-3 py-2 rounded-md text-sm font-medium text-gray-600 hover:bg-gray-100 hover:text-gray-900 transition-colors"
            >
              Sign out
            </button>
          </div>
        </nav>
      </aside>

      <main className="flex-1 overflow-auto">
        <div className="max-w-5xl mx-auto px-6 py-8">
          <Outlet />
        </div>
      </main>
    </div>
  )
}

//go:build enterprise

package http

import (
	"net/http"

	"github.com/authincore/authincore/internal/enterprise"
)

// checkSSOEnforcement returns true (and writes an error) if the login method is
// blocked by the org's SSO enforcement policy. Callers must return immediately
// when this returns true.
//
// Policy-store failures fail closed. A separate, audited break-glass path must
// be used instead of silently bypassing an organisation's authentication policy.
func (s *Server) checkSSOEnforcement(w http.ResponseWriter, r *http.Request, method string) bool {
	if s.deps.LoginPolicies == nil {
		return false
	}
	orgID := orgIDFromRequest(r)
	policy, err := s.deps.LoginPolicies.Get(r.Context(), orgID)
	if err != nil {
		s.deps.Log.Error("sso enforcement: load policy failed", "err", err)
		writeError(w, "SSO policy temporarily unavailable", http.StatusServiceUnavailable)
		return true
	}
	if !enterprise.AllowMethod(policy, method) {
		writeError(w, "SSO is enforced for this organization; use your SSO provider to log in", http.StatusForbidden)
		return true
	}
	return false
}

//go:build !enterprise

package http

import "net/http"

// registerEnterpriseRoutes is a no-op in the OSS build.
// SAML, SCIM, and other enterprise features are not compiled in.
func (s *Server) registerEnterpriseRoutes(_ *http.ServeMux) {}

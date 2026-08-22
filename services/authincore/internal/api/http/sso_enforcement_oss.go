//go:build !enterprise

package http

import "net/http"

// checkSSOEnforcement is a no-op in the OSS build — always allows the method.
func (s *Server) checkSSOEnforcement(_ http.ResponseWriter, _ *http.Request, _ string) bool {
	return false
}

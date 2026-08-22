//go:build enterprise

package http

import (
	"github.com/authincore/authincore/internal/audit"
	"github.com/authincore/authincore/internal/enterprise"
	"github.com/authincore/authincore/internal/scim"
)

// enterpriseDeps holds optional enterprise-only dependencies.
// Embedded in Deps when compiled with the enterprise build tag.
type enterpriseDeps struct {
	LoginPolicies   *enterprise.LoginPolicyStore // optional; nil disables SSO enforcement
	Branding        *enterprise.BrandingStore    // optional; nil disables custom branding
	AuditConfigs    *audit.ConfigStore           // optional; nil disables audit export management
	SCIMTokenIssuer scim.TokenIssuer             // optional; nil disables SCIM token management
}

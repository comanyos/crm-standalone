package oidc

import (
	"net/url"
	"testing"

	"github.com/google/uuid"
)

func TestBuildConsentResumeURLPreservesPublicPKCERequest(t *testing.T) {
	req := &ConsentRequest{
		ID: "consent-1", ClientID: "crm-web", RedirectURI: "http://localhost:3000/auth/callback",
		State: "csrf-state", OrgID: uuid.New(), Scopes: []string{"openid", "offline_access"},
		CodeChallenge: "challenge", CodeChallengeMethod: "S256",
	}
	parsed, err := url.Parse(buildConsentResumeURL(req))
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	for key, want := range map[string]string{
		"consent_id": "consent-1", "response_type": "code", "client_id": "crm-web",
		"redirect_uri": req.RedirectURI, "state": "csrf-state", "scope": "openid offline_access",
		"org_id": req.OrgID.String(), "code_challenge": "challenge", "code_challenge_method": "S256",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

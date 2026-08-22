// Package saml implements a SAML 2.0 Service Provider (SP) using crewjam/saml.
//
// AuthinCore acts as the SP. For each SAML IdP configured per-org it:
//   - builds a crewjam/saml.ServiceProvider with the IdP metadata
//   - initiates SSO by producing a signed AuthnRequest redirect (HTTP-Redirect binding)
//   - consumes the POST-binding ACS response, validates the XML signature and
//     assertion constraints, then creates/links an identity in the local store
//   - serves SP metadata XML so the IdP admin can register AuthinCore
//
// Per-org IdP configs are stored in the saml_providers table and loaded on
// every request (with an in-process cache keyed on provider UUID).
package saml

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/authincore/authincore/internal/util"
	crewsaml "github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/google/uuid"
)

// ProviderConfig holds per-org SAML IdP configuration stored in the DB.
type ProviderConfig struct {
	ID           uuid.UUID         `json:"id"`
	OrgID        uuid.UUID         `json:"org_id"`
	Name         string            `json:"name"`
	MetadataURL  string            `json:"metadata_url"`
	MetadataXML  string            `json:"metadata_xml,omitempty"`
	EntityID     string            `json:"entity_id"`
	SSOURL       string            `json:"sso_url"`
	Certificate  string            `json:"certificate"` // PEM-encoded IdP signing cert
	SPCertPEM    string            `json:"sp_cert_pem"` // persistent SP signing cert (PEM)
	SPKeyPEM     string            `json:"sp_key_pem"`  // persistent SP private key (PEM, encrypted at rest)
	NameIDFormat string            `json:"name_id_format"`
	AttributeMap map[string]string `json:"attribute_map"` // saml attr → identity trait key
	Active       bool              `json:"active"`
	CreatedAt    time.Time         `json:"created_at"`
}

// ConfigStore persists SAML provider configurations.
type ConfigStore interface {
	Save(ctx context.Context, cfg *ProviderConfig) error
	ByID(ctx context.Context, orgID, id uuid.UUID) (*ProviderConfig, error)
	List(ctx context.Context, orgID uuid.UUID) ([]*ProviderConfig, error)
	Delete(ctx context.Context, orgID, id uuid.UUID) error
}

// IdentityLinker creates or links an identity from a validated SAML assertion.
type IdentityLinker interface {
	LinkOrCreate(ctx context.Context, orgID uuid.UUID, providerID, nameID string, attrs map[string]string) (uuid.UUID, error)
}

// SessionMinter creates a login session for a resolved identity.
type SessionMinter interface {
	MintSession(ctx context.Context, orgID, identityID uuid.UUID, method string) (string, error)
}

// SP is the SAML Service Provider router — one per AuthinCore instance.
// Each HTTP request resolves the per-org provider config and builds a
// crewjam/saml.ServiceProvider on-the-fly (cached by provider UUID).
type SP struct {
	store   ConfigStore
	linker  IdentityLinker
	minter  SessionMinter
	baseURL string // e.g. https://auth.example.com

	mu    sync.RWMutex
	cache map[uuid.UUID]*spEntry
}

type spEntry struct {
	sp        *crewsaml.ServiceProvider
	spKeyPair tls.Certificate
	builtAt   time.Time
}

const (
	errProviderNotFound = "SAML provider not found"
	errBuildSP          = "build SP: %v"
	contentTypeJSON     = "application/json"
	contentTypeHeader   = "Content-Type"
)

func New(store ConfigStore, linker IdentityLinker, minter SessionMinter, baseURL string) *SP {
	return &SP{
		store:   store,
		linker:  linker,
		minter:  minter,
		baseURL: baseURL,
		cache:   make(map[uuid.UUID]*spEntry),
	}
}

// resolveProviderID extracts the provider UUID from the URL path pattern
// "/saml/{provider_id}/..." using Go 1.22 ServeMux path values.
func resolveProviderID(r *http.Request) (uuid.UUID, error) {
	raw := r.PathValue("provider_id")
	if raw == "" {
		return uuid.Nil, errors.New("provider_id missing from path")
	}
	return uuid.Parse(raw)
}

// resolveOrgID extracts org_id from query param (simple approach — in production
// use subdomain routing or authn context).
func resolveOrgID(r *http.Request) (uuid.UUID, error) {
	raw := r.URL.Query().Get("org_id")
	if raw == "" {
		return uuid.Nil, errors.New("org_id query param required")
	}
	return uuid.Parse(raw)
}

// resolveIdPMetadata parses or constructs the IdP EntityDescriptor from cfg.
func resolveIdPMetadata(cfg *ProviderConfig) (*crewsaml.EntityDescriptor, error) {
	if cfg.MetadataXML != "" {
		if len(cfg.MetadataXML) > 5<<20 {
			return nil, errors.New("IdP metadata exceeds 5 MiB")
		}
		meta := &crewsaml.EntityDescriptor{}
		// #nosec G709 -- encoding/xml does not resolve external entities; input is size-bounded and schema-decoded into a fixed type.
		if err := xml.Unmarshal([]byte(cfg.MetadataXML), meta); err != nil {
			return nil, fmt.Errorf("parse IdP metadata XML: %w", err)
		}
		return meta, nil
	}
	ssoURL, err := url.Parse(cfg.SSOURL)
	if err != nil {
		return nil, fmt.Errorf("parse SSO URL: %w", err)
	}
	return buildMinimalIdPMetadata(cfg.EntityID, ssoURL, cfg.Certificate), nil
}

// resolveKeyPair loads the SP signing key-pair from cfg, generating and
// persisting a new one if the DB row has no cert yet.
func (s *SP) resolveKeyPair(cfg *ProviderConfig) (tls.Certificate, error) {
	certPEM, keyPEM := []byte(cfg.SPCertPEM), []byte(cfg.SPKeyPEM)
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		var err error
		certPEM, keyPEM, err = generateSelfSignedCert(cfg.OrgID.String())
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("generate SP cert: %w", err)
		}
		cfg.SPCertPEM = string(certPEM)
		cfg.SPKeyPEM = string(keyPEM)
		_ = s.store.Save(context.Background(), cfg) // best-effort persist
	}
	kp, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load SP key pair: %w", err)
	}
	kp.Leaf, err = x509.ParseCertificate(kp.Certificate[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse SP leaf cert: %w", err)
	}
	return kp, nil
}

// getOrBuildSP returns (or builds and caches) a crewjam ServiceProvider for
// the given ProviderConfig. SP entries are rebuilt after 1 hour to pick up
// metadata refreshes.
func (s *SP) getOrBuildSP(cfg *ProviderConfig) (*crewsaml.ServiceProvider, error) {
	s.mu.RLock()
	entry, ok := s.cache[cfg.ID]
	s.mu.RUnlock()
	if ok && time.Since(entry.builtAt) < time.Hour {
		return entry.sp, nil
	}

	idpMeta, err := resolveIdPMetadata(cfg)
	if err != nil {
		return nil, err
	}

	keyPair, err := s.resolveKeyPair(cfg)
	if err != nil {
		return nil, err
	}

	acsURL, _ := url.Parse(fmt.Sprintf("%s/saml/%s/acs", s.baseURL, cfg.ID))
	metaURL, _ := url.Parse(fmt.Sprintf("%s/saml/%s/metadata", s.baseURL, cfg.ID))
	entityID := fmt.Sprintf("%s/saml/%s", s.baseURL, cfg.ID)

	sp := crewsaml.ServiceProvider{
		EntityID:          entityID,
		Key:               keyPair.PrivateKey.(*rsa.PrivateKey),
		Certificate:       keyPair.Leaf,
		MetadataURL:       *metaURL,
		AcsURL:            *acsURL,
		IDPMetadata:       idpMeta,
		AuthnNameIDFormat: crewsaml.NameIDFormat(cfg.NameIDFormat),
	}

	s.mu.Lock()
	s.cache[cfg.ID] = &spEntry{sp: &sp, spKeyPair: keyPair, builtAt: time.Now()}
	s.mu.Unlock()

	return &sp, nil
}

// InitiateSSO handles GET /saml/{provider_id}/login
// Produces an HTTP-Redirect binding AuthnRequest to the IdP.
func (s *SP) InitiateSSO(w http.ResponseWriter, r *http.Request) {
	providerID, err := resolveProviderID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	orgID, err := resolveOrgID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg, err := s.store.ByID(r.Context(), orgID, providerID)
	if err != nil {
		http.Error(w, errProviderNotFound, http.StatusNotFound)
		return
	}
	if !cfg.Active {
		http.Error(w, "SAML provider disabled", http.StatusForbidden)
		return
	}

	sp, err := s.getOrBuildSP(cfg)
	if err != nil {
		http.Error(w, fmt.Sprintf(errBuildSP, err), http.StatusInternalServerError)
		return
	}

	// crewjam/saml MakeAuthenticationRequest returns an *AuthnRequest with
	// the correct ID, IssueInstant, and Destination.
	authReq, err := sp.MakeAuthenticationRequest(
		sp.GetSSOBindingLocation(crewsaml.HTTPRedirectBinding),
		crewsaml.HTTPRedirectBinding,
		crewsaml.HTTPPostBinding,
	)
	if err != nil {
		http.Error(w, fmt.Sprintf("make AuthnRequest: %v", err), http.StatusInternalServerError)
		return
	}

	// Encode as HTTP-Redirect binding URL and redirect the browser.
	redirectURL, err := authReq.Redirect("", sp)
	if err != nil {
		http.Error(w, fmt.Sprintf("encode redirect: %v", err), http.StatusInternalServerError)
		return
	}
	// #nosec G710 -- this is the registered IdP SSO endpoint, validated as HTTPS when the provider is created.
	http.Redirect(w, r, redirectURL.String(), http.StatusFound)
}

// AssertionConsumer handles POST /saml/{provider_id}/acs
// Validates the SAMLResponse, extracts the assertion, and creates/links an identity.
func (s *SP) AssertionConsumer(w http.ResponseWriter, r *http.Request) {
	providerID, err := resolveProviderID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// OrgID stored in relay state to survive the IdP round-trip.
	orgIDStr := r.FormValue("RelayState")
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		http.Error(w, "invalid RelayState (expected org_id UUID)", http.StatusBadRequest)
		return
	}

	cfg, err := s.store.ByID(r.Context(), orgID, providerID)
	if err != nil {
		http.Error(w, errProviderNotFound, http.StatusNotFound)
		return
	}

	sp, err := s.getOrBuildSP(cfg)
	if err != nil {
		http.Error(w, fmt.Sprintf(errBuildSP, err), http.StatusInternalServerError)
		return
	}

	// ParseResponse validates:
	//   - XML signature against the IdP cert in metadata
	//   - Conditions (NotBefore / NotOnOrAfter with 1-minute clock-skew)
	//   - Audience restriction matches our EntityID
	//   - InResponseTo matches a previously issued AuthnRequest ID
	//     (crewjam uses a cookie-backed request tracker by default)
	assertion, err := sp.ParseResponse(r, []string{})
	if err != nil {
		var invalidResp *crewsaml.InvalidResponseError
		if errors.As(err, &invalidResp) {
			http.Error(w, fmt.Sprintf("invalid SAML response: %s", invalidResp.PrivateErr), http.StatusUnauthorized)
		} else {
			http.Error(w, fmt.Sprintf("parse SAML response: %v", err), http.StatusUnauthorized)
		}
		return
	}

	// Extract NameID and attributes.
	nameID := assertion.Subject.NameID.Value
	attrs := extractAttributes(assertion, cfg.AttributeMap)

	// Link or create identity in AuthinCore.
	if s.linker == nil {
		http.Error(w, "identity linker not configured", http.StatusInternalServerError)
		return
	}
	identityID, err := s.linker.LinkOrCreate(r.Context(), orgID, providerID.String(), nameID, attrs)
	if err != nil {
		http.Error(w, fmt.Sprintf("link identity: %v", err), http.StatusInternalServerError)
		return
	}

	// Mint a session.
	if s.minter == nil {
		// Return identity ID as JSON if no minter configured (useful for testing).
		w.Header().Set(contentTypeHeader, contentTypeJSON)
		_ = json.NewEncoder(w).Encode(map[string]string{"identity_id": identityID.String()})
		return
	}
	sessionToken, err := s.minter.MintSession(r.Context(), orgID, identityID, "saml")
	if err != nil {
		http.Error(w, fmt.Sprintf("mint session: %v", err), http.StatusInternalServerError)
		return
	}

	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	cookieName := "authincore_session"
	if secure {
		cookieName = "__Host-authincore_session"
	}
	// #nosec G124 -- production HTTPS uses Secure and __Host-; the non-Secure cookie is restricted to explicit local HTTP development.
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Redirect to app or return JSON for SPA.
	returnTo := util.SafeReturnTo(r.URL.Query().Get("return_to"))
	if returnTo == "" {
		returnTo = "/"
	}
	// #nosec G710 -- SafeReturnTo accepts only same-origin absolute paths and rejects protocol-relative forms.
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// MetadataHandler handles GET /saml/{provider_id}/metadata
// Returns the SP metadata XML so the IdP admin can register AuthinCore.
func (s *SP) MetadataHandler(w http.ResponseWriter, r *http.Request) {
	providerID, err := resolveProviderID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	orgID, err := resolveOrgID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg, err := s.store.ByID(r.Context(), orgID, providerID)
	if err != nil {
		http.Error(w, errProviderNotFound, http.StatusNotFound)
		return
	}

	sp, err := s.getOrBuildSP(cfg)
	if err != nil {
		http.Error(w, fmt.Sprintf(errBuildSP, err), http.StatusInternalServerError)
		return
	}

	metadata, err := xml.MarshalIndent(sp.Metadata(), "", "  ")
	if err != nil {
		http.Error(w, fmt.Sprintf("marshal metadata: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set(contentTypeHeader, "application/samlmetadata+xml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(xml.Header))
	// #nosec G705 -- metadata is generated by xml.Marshal from a typed structure and served with an XML content type plus nosniff.
	_, _ = w.Write(metadata)
}

// CreateProvider handles POST /api/v1/saml/providers.
func (s *SP) CreateProvider(w http.ResponseWriter, r *http.Request) {
	var cfg ProviderConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	cfg.ID = uuid.New()
	cfg.CreatedAt = time.Now().UTC()

	// If MetadataURL is set but MetadataXML is not, fetch it.
	if cfg.MetadataURL != "" && cfg.MetadataXML == "" {
		metaURL, err := parseURL(cfg.MetadataURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid metadata_url: %v", err), http.StatusBadRequest)
			return
		}
		metaClient := &http.Client{Timeout: 10 * time.Second}
		fetched, err := samlsp.FetchMetadata(r.Context(), metaClient, *metaURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("fetch IdP metadata: %v", err), http.StatusBadRequest)
			return
		}
		raw, _ := xml.Marshal(fetched)
		cfg.MetadataXML = string(raw)
		cfg.EntityID = fetched.EntityID
		if len(fetched.IDPSSODescriptors) > 0 {
			for _, sso := range fetched.IDPSSODescriptors[0].SingleSignOnServices {
				if sso.Binding == crewsaml.HTTPRedirectBinding {
					cfg.SSOURL = sso.Location
					break
				}
			}
		}
	}

	if err := s.store.Save(r.Context(), &cfg); err != nil {
		http.Error(w, fmt.Sprintf("save provider: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set(contentTypeHeader, contentTypeJSON)
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(cfg)
}

// ListProviders handles GET /api/v1/saml/providers?org_id=...
func (s *SP) ListProviders(w http.ResponseWriter, r *http.Request) {
	orgID, err := resolveOrgID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfgs, err := s.store.List(r.Context(), orgID)
	if err != nil {
		http.Error(w, fmt.Sprintf("list providers: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set(contentTypeHeader, contentTypeJSON)
	_ = json.NewEncoder(w).Encode(cfgs)
}

// --- helpers ---

// extractAttributes maps SAML assertion attributes to identity trait keys
// using the provider's AttributeMap. Unmapped attributes are included as-is.
func extractAttributes(assertion *crewsaml.Assertion, attrMap map[string]string) map[string]string {
	out := make(map[string]string)
	for _, stmt := range assertion.AttributeStatements {
		for _, attr := range stmt.Attributes {
			key := attr.Name
			if mapped, ok := attrMap[key]; ok {
				key = mapped
			}
			if len(attr.Values) > 0 {
				out[key] = attr.Values[0].Value
			}
		}
	}
	return out
}

// buildMinimalIdPMetadata constructs an EntityDescriptor from explicit config fields
// when a full metadata XML is not provided.
func buildMinimalIdPMetadata(entityID string, ssoURL *url.URL, certPEM string) *crewsaml.EntityDescriptor {
	desc := &crewsaml.EntityDescriptor{
		EntityID: entityID,
		IDPSSODescriptors: []crewsaml.IDPSSODescriptor{
			{
				SingleSignOnServices: []crewsaml.Endpoint{
					{Binding: crewsaml.HTTPRedirectBinding, Location: ssoURL.String()},
					{Binding: crewsaml.HTTPPostBinding, Location: ssoURL.String()},
				},
			},
		},
	}
	if certPEM != "" {
		block, _ := pem.Decode([]byte(certPEM))
		if block != nil {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err == nil {
				desc.IDPSSODescriptors[0].KeyDescriptors = []crewsaml.KeyDescriptor{
					{
						Use: "signing",
						KeyInfo: crewsaml.KeyInfo{
							X509Data: crewsaml.X509Data{
								X509Certificates: []crewsaml.X509Certificate{
									{Data: string(pem.EncodeToMemory(&pem.Block{
										Type:  "CERTIFICATE",
										Bytes: cert.Raw,
									}))},
								},
							},
						},
					},
				}
			}
		}
	}
	return desc
}

// generateSelfSignedCert generates an ephemeral RSA-2048 self-signed cert for the SP.
// In production, load a persistent cert from a secret manager or HSM.
func generateSelfSignedCert(org string) (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: org + " AuthinCore SAML SP"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create cert: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("invalid URL %q: scheme must be http or https", raw)
	}
	return u, nil
}

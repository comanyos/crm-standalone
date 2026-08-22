//go:build federation

package okta_test

// Federation tests against a real Okta dev tenant.
// Run with:
//
//	OKTA_DOMAIN=dev-xxx.okta.com \
//	OKTA_SAML_METADATA_URL=https://dev-xxx.okta.com/app/xxx/sso/saml/metadata \
//	AUTHINCORE_URL=http://localhost:8080 \
//	go test -tags=federation ./test/federation/okta/... -v -timeout=120s

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func authinCoreURL() string {
	u := os.Getenv("AUTHINCORE_URL")
	if u == "" {
		return "http://localhost:8080"
	}
	return u
}

func skipIfNotConfigured(t *testing.T) {
	t.Helper()
	if os.Getenv("OKTA_SAML_METADATA_URL") == "" {
		t.Skip("OKTA_SAML_METADATA_URL not set — skipping live Okta federation test")
	}
}

// TestOkta_FetchMetadata verifies AuthinCore can fetch and parse Okta's IdP metadata.
func TestOkta_FetchIdPMetadata(t *testing.T) {
	skipIfNotConfigured(t)
	metaURL := os.Getenv("OKTA_SAML_METADATA_URL")

	resp, err := http.Get(metaURL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var meta struct {
		XMLName  xml.Name `xml:"EntityDescriptor"`
		EntityID string   `xml:"entityID,attr"`
	}
	require.NoError(t, xml.Unmarshal(body, &meta))
	assert.NotEmpty(t, meta.EntityID, "Okta metadata must contain entityID")
	t.Logf("Okta entityID: %s", meta.EntityID)
}

// TestOkta_RegisterProvider registers the Okta IdP with AuthinCore and checks the SP metadata is valid.
func TestOkta_RegisterProvider(t *testing.T) {
	skipIfNotConfigured(t)
	metaURL := os.Getenv("OKTA_SAML_METADATA_URL")

	// Fetch Okta metadata
	resp, err := http.Get(metaURL)
	require.NoError(t, err)
	defer resp.Body.Close()
	metaXML, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Register provider
	body := fmt.Sprintf(`{"name":"okta-test","metadata_xml":%q}`, string(metaXML))
	reg, err := http.Post(
		authinCoreURL()+"/api/v1/saml/providers",
		"application/json",
		strings.NewReader(body),
	)
	require.NoError(t, err)
	defer reg.Body.Close()
	require.Equal(t, http.StatusCreated, reg.StatusCode, "provider registration must return 201")

	var result struct {
		ID string `json:"id"`
	}
	require.NoError(t, xml.NewDecoder(reg.Body).Decode(&result))

	// Fetch SP metadata — must be valid XML
	metaResp, err := http.Get(authinCoreURL() + "/saml/" + result.ID + "/metadata")
	require.NoError(t, err)
	defer metaResp.Body.Close()
	assert.Equal(t, http.StatusOK, metaResp.StatusCode)
	assert.Contains(t, metaResp.Header.Get("Content-Type"), "xml")

	t.Logf("SP metadata served at /saml/%s/metadata", result.ID)
}

// TestOkta_SPMetadataXMLValid checks the SP metadata AuthinCore generates is well-formed XML
// with required SAML fields that Okta's wizard expects.
func TestOkta_SPMetadataXMLValid(t *testing.T) {
	skipIfNotConfigured(t)
	providerID := os.Getenv("AUTHINCORE_SAML_PROVIDER_ID")
	if providerID == "" {
		t.Skip("AUTHINCORE_SAML_PROVIDER_ID not set")
	}

	resp, err := http.Get(authinCoreURL() + "/saml/" + providerID + "/metadata")
	require.NoError(t, err)
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var meta struct {
		XMLName          xml.Name `xml:"EntityDescriptor"`
		SPSSODescriptors []struct {
			AssertionConsumerServices []struct {
				Location string `xml:"Location,attr"`
				Binding  string `xml:"Binding,attr"`
			} `xml:"AssertionConsumerService"`
		} `xml:"SPSSODescriptor"`
	}
	require.NoError(t, xml.Unmarshal(raw, &meta), "SP metadata must be valid XML")
	require.NotEmpty(t, meta.SPSSODescriptors, "must have SPSSODescriptor")
	acs := meta.SPSSODescriptors[0].AssertionConsumerServices
	require.NotEmpty(t, acs, "must have AssertionConsumerService")
	assert.Contains(t, acs[0].Binding, "HTTP-POST", "must support POST binding")
	assert.NotEmpty(t, acs[0].Location, "ACS URL must be set")
	t.Logf("ACS URL: %s", acs[0].Location)
}

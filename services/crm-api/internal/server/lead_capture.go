package server

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/integration/leadcapture"
)

// leadCapturePrefix is where third-party systems POST leads. The endpoint's
// unguessable token is the rest of the path, and it is also how the tenant is
// resolved — the request never names one.
const leadCapturePrefix = "/integrations/lead-capture/"

// leadCaptureHandler receives one delivery.
//
// This is the second route outside the authenticating interceptor, after the
// Google OAuth callback, and it is unauthenticated for a comparable reason:
// the caller is a third-party server that cannot hold a CRM session. What
// stands in for authentication is not in this file's hands — an unguessable
// token, an HMAC signature over the exact bytes, and a dedup key, all checked
// in internal/integration/leadcapture.
//
// What this file *is* responsible for is that nothing an attacker controls
// reaches the response, and that the response says as little as possible. A
// well-behaved provider needs exactly one thing from us: whether to retry.
// Everything finer is an oracle — most importantly, an unknown token and a
// bad signature must be indistinguishable, or the response can be used to
// test whether a token is real.
func (s *Server) leadCaptureHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		token := strings.TrimPrefix(r.URL.Path, leadCapturePrefix)
		if token == "" || strings.Contains(token, "/") {
			// Not even shaped like a token. Answered the same way an unknown
			// one is, for the same reason.
			w.WriteHeader(http.StatusAccepted)
			return
		}

		// The body is read under a hard cap before anything else touches it.
		// MaxBytesReader rather than a length check, because Content-Length is
		// the caller's claim and the body is the caller's stream.
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, domain.MaxCaptureBodyBytes))
		if err != nil {
			// The one case worth a distinct status: a provider sending too
			// much needs to know, and the size limit is public in
			// docs/LEAD_CAPTURE.md, so saying so reveals nothing.
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}

		result := s.leadCapture.Deliver(r.Context(), token, body,
			leadcapture.NormalizeSignature(r.Header.Get(leadcapture.SignatureHeader)))

		if result.Reason != "" && result.Reason != domain.DeliveryDuplicate {
			// The request id ties this to the access log; the reason is
			// already bounded, and no part of the payload is logged here —
			// the delivery row holds that, under the tenant's own scope.
			slog.Warn("a lead capture delivery did not create a lead",
				"reason", result.Reason, "request_id", RequestID(r.Context()))
		}

		// A transient failure after a request has proved it holds the endpoint
		// secret must be retried. Returning 202 here used to acknowledge and
		// permanently lose leads when CreateLead failed.
		if !result.Accepted {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}

		// 202 for everything the service handled, including the rejections.
		// A provider that gets an error retries, and retrying a bad signature
		// forever helps nobody; the tenant sees the real outcome in the
		// delivery log. "Accepted" is also the honest word for a lead created
		// synchronously today that may be queued tomorrow.
		w.WriteHeader(http.StatusAccepted)
	})
}

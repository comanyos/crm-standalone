package workflow

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/santoshguru/crm/internal/domain"
	"github.com/santoshguru/crm/internal/secrets"
)

const webhookUserAgent = "Savvy-CRM-Workflow/1.0"

// WebhookSecretBinding is the authenticated-data binding used both when the
// API seals an endpoint secret and when the worker opens it. Tenant and row id
// are both included so moving ciphertext between rows or organizations makes
// it unusable rather than turning it into a valid credential elsewhere.
func WebhookSecretBinding(tenantID, endpointID string) []byte {
	return []byte("workflow-webhook\x00" + tenantID + "\x00" + endpointID)
}

// WebhookSender delivers small, signed workflow notifications. The receiver
// gets a stable event id and may safely deduplicate retries.
type WebhookSender struct {
	store  domain.Store
	sealer *secrets.Sealer
	client *http.Client
	now    func() time.Time
}

func NewWebhookSender(store domain.Store, sealer *secrets.Sealer, client *http.Client) *WebhookSender {
	if store == nil || sealer == nil {
		return nil
	}
	if client == nil {
		client = safeWebhookClient()
	}
	return &WebhookSender{store: store, sealer: sealer, client: client, now: func() time.Time { return time.Now().UTC() }}
}

type webhookPayload struct {
	ID          string    `json:"id"`
	Event       string    `json:"event"`
	OccurredAt  time.Time `json:"occurred_at"`
	RuleID      string    `json:"rule_id"`
	RuleVersion string    `json:"rule_version_id"`
	ExecutionID string    `json:"execution_id"`
	SubjectType string    `json:"subject_type"`
	SubjectID   string    `json:"subject_id"`
}

func (s *WebhookSender) Deliver(
	ctx context.Context, scope domain.Scope, endpointID string,
	version *domain.WorkflowRuleVersion, execution *domain.WorkflowExecution, actionIndex int,
) error {
	if s == nil || s.sealer == nil {
		return fmt.Errorf("%w: webhook delivery is not configured", domain.ErrInvalid)
	}
	endpoint, err := s.store.GetWorkflowWebhookEndpoint(ctx, scope, endpointID)
	if err != nil {
		return err
	}
	if err := endpoint.Validate(); err != nil {
		return err
	}
	secret, err := s.sealer.Open(endpoint.SigningSecretCiphertext, WebhookSecretBinding(scope.TenantID, endpoint.ID))
	if err != nil {
		return fmt.Errorf("open webhook signing secret: %w", err)
	}
	defer zeroBytes(secret)

	now := s.now().UTC()
	eventID := execution.ID + ":" + strconv.Itoa(actionIndex)
	body, err := json.Marshal(webhookPayload{
		ID: eventID, Event: "workflow.action", OccurredAt: now,
		RuleID: version.RuleID, RuleVersion: version.ID, ExecutionID: execution.ID,
		SubjectType: execution.SubjectType, SubjectID: execution.SubjectID,
	})
	if err != nil {
		return fmt.Errorf("encode webhook payload: %w", err)
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", webhookUserAgent)
	req.Header.Set("X-Savvy-Event-ID", eventID)
	req.Header.Set("X-Savvy-Timestamp", timestamp)
	req.Header.Set("X-Savvy-Signature", signature)

	status, responseCode := "failed", 0
	resp, sendErr := s.client.Do(req)
	if resp != nil {
		responseCode = resp.StatusCode
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if responseCode >= 200 && responseCode < 300 {
			status = "delivered"
		}
	}
	recordErr := s.store.RecordWorkflowWebhookDelivery(ctx, scope, &domain.WorkflowWebhookDelivery{
		EndpointID: endpoint.ID, ExecutionID: execution.ID, ActionIndex: actionIndex,
		Status: status, ResponseCode: responseCode, AttemptedAt: now,
	})
	if sendErr != nil {
		if recordErr != nil {
			return fmt.Errorf("deliver webhook: %v; record delivery: %w", sendErr, recordErr)
		}
		return fmt.Errorf("deliver webhook: %w", sendErr)
	}
	if recordErr != nil {
		return fmt.Errorf("record webhook delivery: %w", recordErr)
	}
	if status != "delivered" {
		return fmt.Errorf("webhook returned HTTP %d", responseCode)
	}
	return nil
}

func safeWebhookClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve webhook host: %w", err)
		}
		if len(addresses) == 0 {
			return nil, errors.New("webhook host resolved to no addresses")
		}
		for _, candidate := range addresses {
			if domain.UnsafeWebhookIP(candidate.IP) {
				return nil, errors.New("webhook host resolved to a private or local address")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

//go:build smtp_live

package auth

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveSMTPDelivery is an explicit operator probe. It is excluded from the
// normal test suite and requires a real credential and recipient. Keeping the
// probe beside the production mailer exercises the exact TLS, AUTH, envelope,
// DATA, and QUIT path instead of proving only that a provider accepts a login.
func TestLiveSMTPDelivery(t *testing.T) {
	password := os.Getenv("SMTP_TEST_PASSWORD")
	recipient := os.Getenv("SMTP_TEST_RECIPIENT")
	if password == "" || recipient == "" {
		t.Fatal("SMTP_TEST_PASSWORD and SMTP_TEST_RECIPIENT are required")
	}

	sender, err := newSMTPMailer(SMTPConfig{
		Host:     "smtp.gmail.com",
		Port:     465,
		Username: "support@savvyadvi.com",
		Password: password,
		From:     "Savvy CRM <support@savvyadvi.com>",
		UseTLS:   true,
		Timeout:  15 * time.Second,
	})
	if err != nil {
		t.Fatalf("configure live SMTP probe: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := sender.Send(ctx, recipient, "Savvy CRM Go SMTP deployment test",
		"The production Go SMTP path completed successfully."); err != nil {
		t.Fatalf("live SMTP delivery: %v", err)
	}
}

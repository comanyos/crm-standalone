package auth

import (
	"context"
	"strings"
	"testing"
)

func validSMTPConfig() SMTPConfig {
	return SMTPConfig{
		Host: "smtp.example.com", Port: 587,
		Username: "support@example.com", Password: "provider-secret",
		From: "Savvy CRM <support@example.com>", RequireSTARTTLS: true,
	}
}

func TestSMTPConfigurationFailsClosed(t *testing.T) {
	tests := map[string]func(*SMTPConfig){
		"port in host": func(c *SMTPConfig) { c.Host = "smtp.example.com:587" },
		"both TLS modes": func(c *SMTPConfig) {
			c.UseTLS = true
			c.RequireSTARTTLS = true
		},
		"partial credentials": func(c *SMTPConfig) { c.Password = "" },
		"invalid from":        func(c *SMTPConfig) { c.From = "not an address" },
		"injected from": func(c *SMTPConfig) {
			c.From = "Savvy CRM <support@example.com>\r\nRCPT TO:<attacker@example.com>"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validSMTPConfig()
			mutate(&cfg)
			if _, err := newSMTPMailer(cfg); err == nil {
				t.Fatal("invalid SMTP configuration was accepted")
			}
		})
	}
}

func TestSMTPRejectsHeaderInjectionBeforeDial(t *testing.T) {
	sender, err := newSMTPMailer(validSMTPConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), "person@example.com", "Code\r\nBcc: attacker@example.com", "body"); err == nil {
		t.Fatal("SMTP subject header injection was accepted")
	}
	if err := sender.Send(context.Background(), "person@example.com\r\nRCPT TO:<attacker@example.com>", "Code", "body"); err == nil {
		t.Fatal("SMTP recipient command injection was accepted")
	}
}

func TestSMTPMessageUsesCRLFAndEncodedSubject(t *testing.T) {
	message := smtpMessage("Savvy CRM <support@example.com>", "person@example.com", "Your Savvy code — 123456", "line one\nline two")
	if strings.Contains(strings.ReplaceAll(message, "\r\n", ""), "\n") {
		t.Fatal("SMTP message contains a bare LF")
	}
	if !strings.Contains(message, "Subject: =?UTF-8?") || !strings.Contains(message, "line one\r\nline two\r\n") {
		t.Fatalf("unexpected SMTP message: %q", message)
	}
}

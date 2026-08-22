// Package smtp implements courier.Sender backed by a standard SMTP relay.
package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// Config holds SMTP connection settings.
type Config struct {
	Host     string // e.g. smtp.sendgrid.net
	Port     int    // 587 (STARTTLS) or 465 (TLS)
	Username string
	Password string
	From     string // "AuthinCore <no-reply@example.com>"
	// UseTLS uses implicit TLS (port 465). Default false → STARTTLS on port 587.
	UseTLS bool
}

// Sender delivers email via SMTP.
type Sender struct {
	cfg Config
}

func New(cfg Config) *Sender {
	return &Sender{cfg: cfg}
}

// SendEmail delivers a plain-text + HTML email to a single recipient.
func (s *Sender) SendEmail(_ context.Context, to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)

	msg := buildMessage(s.cfg.From, to, subject, body)

	if s.cfg.UseTLS {
		return s.sendTLS(addr, auth, to, msg)
	}
	return smtp.SendMail(addr, auth, extractAddr(s.cfg.From), []string{to}, []byte(msg))
}

// SendSMS is not implemented — stub satisfies courier.Sender.
func (s *Sender) SendSMS(_ context.Context, _, _ string) error {
	return fmt.Errorf("smtp sender does not support SMS")
}

func (s *Sender) sendTLS(addr string, auth smtp.Auth, to, msg string) error {
	host, _, _ := net.SplitHostPort(addr)
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return fmt.Errorf("smtp tls dial: %w", err)
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("smtp new client: %w", err)
	}
	defer client.Close()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(extractAddr(s.cfg.From)); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	defer wc.Close()
	if _, err := fmt.Fprint(wc, msg); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	return nil
}

func buildMessage(from, to, subject, body string) string {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body)
	return sb.String()
}

// extractAddr strips the display name from "Name <addr@host>" → "addr@host".
func extractAddr(from string) string {
	if i := strings.Index(from, "<"); i != -1 {
		return strings.TrimRight(from[i+1:], ">")
	}
	return from
}

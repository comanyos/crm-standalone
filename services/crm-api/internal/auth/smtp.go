package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type smtpMailer struct {
	cfg          SMTPConfig
	from         *mail.Address
	envelopeFrom string
}

func newSMTPMailer(cfg SMTPConfig) (*smtpMailer, error) {
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.From = strings.TrimSpace(cfg.From)
	if cfg.Host == "" || strings.ContainsAny(cfg.Host, " \t\r\n/") {
		return nil, errorsNewSMTP("host is required and must not contain whitespace or a URL path")
	}
	bareHost := strings.Trim(cfg.Host, "[]")
	if strings.Contains(bareHost, ":") && net.ParseIP(bareHost) == nil {
		return nil, errorsNewSMTP("host must not include a port")
	}
	cfg.Host = bareHost
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, errorsNewSMTP("port must be between 1 and 65535")
	}
	if cfg.UseTLS && cfg.RequireSTARTTLS {
		return nil, errorsNewSMTP("implicit TLS and STARTTLS cannot both be enabled")
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, errorsNewSMTP("username and password must be configured together")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid SMTP_FROM: %w", err)
	}
	envelopeFrom, err := normalizeEmail(from.Address)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid SMTP_FROM envelope: %w", err)
	}
	// Rebuild the parsed address from the normalized, line-break-free mailbox
	// while retaining the optional human-readable display name for headers.
	from.Address = envelopeFrom
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	return &smtpMailer{cfg: cfg, from: from, envelopeFrom: envelopeFrom}, nil
}

func errorsNewSMTP(message string) error { return fmt.Errorf("auth: SMTP %s", message) }

func (s *smtpMailer) Send(ctx context.Context, to, subject, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.ContainsAny(subject, "\r\n") {
		return errorsNewSMTP("subject must not contain line breaks")
	}
	recipientAddress, err := normalizeEmail(to)
	if err != nil {
		return fmt.Errorf("auth: invalid SMTP recipient: %w", err)
	}
	recipient := &mail.Address{Address: recipientAddress}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	conn, err := s.dial(ctx, addr)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck

	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("auth: SMTP set deadline: %w", err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-done:
		}
	}()
	defer close(done)

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("auth: SMTP client: %w", err)
	}
	defer client.Close() //nolint:errcheck
	if !s.cfg.UseTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("auth: SMTP STARTTLS: %w", err)
			}
		} else if s.cfg.RequireSTARTTLS {
			return errorsNewSMTP("server does not offer required STARTTLS")
		}
	}
	if s.cfg.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("auth: SMTP authentication: %w", err)
		}
	}
	// envelopeFrom passed normalizeEmail during construction and therefore
	// cannot contain the CR/LF command delimiters G707 is designed to catch;
	// net/smtp.Client.Mail independently rejects them as defense in depth.
	// #nosec G707 -- The envelope address is strictly parsed and normalized above.
	if err := client.Mail(s.envelopeFrom); err != nil {
		return fmt.Errorf("auth: SMTP MAIL FROM: %w", err)
	}
	if err := client.Rcpt(recipient.Address); err != nil {
		return fmt.Errorf("auth: SMTP RCPT TO: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("auth: SMTP DATA: %w", err)
	}
	message := smtpMessage(s.from.String(), recipient.String(), subject, body)
	if _, err := fmt.Fprint(w, message); err != nil {
		_ = w.Close()
		return fmt.Errorf("auth: SMTP write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("auth: SMTP finish: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("auth: SMTP quit: %w", err)
	}
	return nil
}

func (s *smtpMailer) dial(ctx context.Context, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: s.cfg.Timeout}
	if s.cfg.UseTLS {
		conn, err := (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{
			ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12,
		}}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("auth: SMTP TLS dial: %w", err)
		}
		return conn, nil
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("auth: SMTP dial: %w", err)
	}
	return conn, nil
}

func smtpMessage(from, to, subject, body string) string {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	return "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" + body + "\r\n"
}

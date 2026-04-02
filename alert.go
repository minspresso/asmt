package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Alerter sends notifications when check statuses change.
type Alerter interface {
	Alert(ctx context.Context, result CheckResult, previousStatus Status) error
}

// MultiAlerter fans out alerts to multiple alerters.
type MultiAlerter struct {
	alerters []Alerter
}

func NewMultiAlerter(alerters ...Alerter) *MultiAlerter {
	return &MultiAlerter{alerters: alerters}
}

func (m *MultiAlerter) Alert(ctx context.Context, result CheckResult, previousStatus Status) error {
	var errs []string
	for _, a := range m.alerters {
		if err := a.Alert(ctx, result, previousStatus); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("alert errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// LogAlerter logs status changes via slog.
type LogAlerter struct {
	Logger *slog.Logger
}

func (a *LogAlerter) Alert(_ context.Context, result CheckResult, previousStatus Status) error {
	a.Logger.Warn("status change",
		"component", result.Component,
		"status", result.Status.String(),
		"previous", previousStatus.String(),
		"message", result.Message,
	)
	return nil
}

// WebhookAlerter posts JSON to a webhook URL (works with Slack, Discord, etc.).
type WebhookAlerter struct {
	URL    string
	client *http.Client
}

func NewWebhookAlerter(url string) *WebhookAlerter {
	return &WebhookAlerter{
		URL:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type webhookPayload struct {
	Text string `json:"text"`
}

func (a *WebhookAlerter) Alert(ctx context.Context, result CheckResult, previousStatus Status) error {
	icon := "🟢"
	switch result.Status {
	case StatusWarn:
		icon = "🟡"
	case StatusCritical:
		icon = "🔴"
	case StatusUnknown:
		icon = "⚪"
	}

	text := fmt.Sprintf("%s *%s*: %s → %s\n%s",
		icon, result.Component,
		previousStatus.String(), result.Status.String(),
		result.Message)

	payload := webhookPayload{Text: text}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", a.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// EmailAlerter sends alerts via SMTP.
type EmailAlerter struct {
	Host     string
	Port     int
	From     string
	To       []string
	Username string
	Password string
}

func (a *EmailAlerter) Alert(_ context.Context, result CheckResult, previousStatus Status) error {
	subject := fmt.Sprintf("[Server-Stat] %s: %s → %s",
		result.Component, previousStatus.String(), result.Status.String())

	body := fmt.Sprintf("Component: %s\nStatus: %s (was %s)\nMessage: %s\nTime: %s\n",
		result.Component,
		result.Status.String(),
		previousStatus.String(),
		result.Message,
		result.CheckedAt.Format(time.RFC3339),
	)

	if len(result.Details) > 0 {
		body += "\nDetails:\n"
		for k, v := range result.Details {
			body += fmt.Sprintf("  %s: %s\n", k, v)
		}
	}

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		a.From,
		strings.Join(a.To, ", "),
		subject,
		body,
	)

	addr := fmt.Sprintf("%s:%d", a.Host, a.Port)
	var auth smtp.Auth
	if a.Username != "" {
		auth = smtp.PlainAuth("", a.Username, a.Password, a.Host)
	}

	return smtp.SendMail(addr, auth, a.From, a.To, []byte(msg))
}

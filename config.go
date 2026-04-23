package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

var appConfig = defaultAppConfig()

type AppConfig struct {
	SessionSecret string       `json:"session_secret"`
	EmailProvider string       `json:"email_provider"`
	SMTP          SMTPConfig   `json:"smtp"`
	Resend        ResendConfig `json:"resend"`
	Admin         AdminConfig  `json:"admin"`
}

type SMTPConfig struct {
	Host      string `json:"host"`
	Port      string `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	FromEmail string `json:"from_email"`
}

type AdminConfig struct {
	Password string `json:"password"`
}

type ResendConfig struct {
	APIKey    string `json:"api_key"`
	FromEmail string `json:"from_email"`
}

func defaultAppConfig() AppConfig {
	return AppConfig{
		SessionSecret: "dev-session-secret-change-me",
		EmailProvider: "smtp",
		SMTP: SMTPConfig{
			Port: "587",
		},
	}
}

func loadAppConfig(path string) AppConfig {
	cfg := defaultAppConfig()

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("config file %q not found; using defaults", path)
			applyEnvOverrides(&cfg)
			appConfig = cfg
			return cfg
		}
		log.Fatalf("failed to read config file %q: %v", path, err)
	}

	if err := json.Unmarshal(content, &cfg); err != nil {
		log.Fatalf("failed to parse config file %q: %v", path, err)
	}

	cfg.SessionSecret = strings.TrimSpace(cfg.SessionSecret)
	if cfg.SessionSecret == "" {
		cfg.SessionSecret = "dev-session-secret-change-me"
	}
	cfg.EmailProvider = strings.ToLower(strings.TrimSpace(cfg.EmailProvider))
	if cfg.EmailProvider == "" {
		cfg.EmailProvider = "smtp"
	}

	cfg.SMTP.Host = strings.TrimSpace(cfg.SMTP.Host)
	cfg.SMTP.Port = strings.TrimSpace(cfg.SMTP.Port)
	cfg.SMTP.Username = strings.TrimSpace(cfg.SMTP.Username)
	cfg.SMTP.FromEmail = strings.TrimSpace(cfg.SMTP.FromEmail)
	if cfg.SMTP.Port == "" {
		cfg.SMTP.Port = "587"
	}

	cfg.Admin.Password = strings.TrimSpace(cfg.Admin.Password)
	cfg.Resend.APIKey = strings.TrimSpace(cfg.Resend.APIKey)
	cfg.Resend.FromEmail = strings.TrimSpace(cfg.Resend.FromEmail)

	applyEnvOverrides(&cfg)

	appConfig = cfg
	return cfg
}

func applyEnvOverrides(cfg *AppConfig) {
	if value := strings.TrimSpace(os.Getenv("SESSION_SECRET")); value != "" {
		cfg.SessionSecret = value
	}
	if value := strings.TrimSpace(os.Getenv("EMAIL_PROVIDER")); value != "" {
		cfg.EmailProvider = strings.ToLower(value)
	}

	if value := strings.TrimSpace(os.Getenv("SMTP_HOST")); value != "" {
		cfg.SMTP.Host = value
	}
	if value := strings.TrimSpace(os.Getenv("SMTP_PORT")); value != "" {
		cfg.SMTP.Port = value
	}
	if value := strings.TrimSpace(os.Getenv("SMTP_USERNAME")); value != "" {
		cfg.SMTP.Username = value
	}
	if value := os.Getenv("SMTP_PASSWORD"); value != "" {
		cfg.SMTP.Password = value
	}
	if value := strings.TrimSpace(os.Getenv("SMTP_FROM_EMAIL")); value != "" {
		cfg.SMTP.FromEmail = value
	}
	if value := os.Getenv("ADMIN_PASSWORD"); value != "" {
		cfg.Admin.Password = strings.TrimSpace(value)
	}
	if value := os.Getenv("RESEND_API_KEY"); value != "" {
		cfg.Resend.APIKey = strings.TrimSpace(value)
	}
	if value := strings.TrimSpace(os.Getenv("RESEND_FROM_EMAIL")); value != "" {
		cfg.Resend.FromEmail = value
	}

	if cfg.SMTP.Port == "" {
		cfg.SMTP.Port = "587"
	}
	if cfg.EmailProvider == "" {
		cfg.EmailProvider = "smtp"
	}

	// Railway exposes a PORT env var. We don't store it in config, but validating
	// it here makes startup issues easier to spot if someone reuses the value.
	if value := strings.TrimSpace(os.Getenv("PORT")); value != "" {
		if _, err := strconv.Atoi(value); err != nil {
			log.Printf("ignoring non-numeric PORT value %q", value)
		}
	}
}

func (c AppConfig) sendMail(recipientEmail string, body string) error {
	_, err := c.sendMailWithTrace(recipientEmail, body)
	return err
}

func (c AppConfig) sendMailWithTrace(recipientEmail string, body string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(c.EmailProvider)) {
	case "", "smtp":
		return c.SMTP.sendMailWithTrace(recipientEmail, body)
	case "resend":
		return c.Resend.sendMailWithTrace(recipientEmail, body)
	default:
		return []string{
			fmt.Sprintf("%s Unknown email provider %q", time.Now().UTC().Format(time.RFC3339), c.EmailProvider),
		}, fmt.Errorf("unsupported email provider %q", c.EmailProvider)
	}
}

func (c SMTPConfig) isConfigured() bool {
	return c.Host != "" && c.FromEmail != ""
}

func (c ResendConfig) isConfigured() bool {
	return c.APIKey != "" && c.FromEmail != ""
}

func (c SMTPConfig) sendMailWithTrace(recipientEmail string, body string) ([]string, error) {
	logs := []string{}
	addLog := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf("%s %s", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...)))
	}

	if !c.isConfigured() {
		addLog("SMTP config is incomplete: host=%q from_email=%q", c.Host, c.FromEmail)
		return logs, fmt.Errorf("email delivery is not configured")
	}

	addLog("Preparing SMTP test send")
	addLog("Host=%q Port=%q Username=%q From=%q To=%q", c.Host, c.Port, c.Username, c.FromEmail, recipientEmail)

	address := net.JoinHostPort(c.Host, c.Port)
	addLog("Dialing %s", address)
	conn, err := net.DialTimeout("tcp", address, 15*time.Second)
	if err != nil {
		addLog("Dial failed: %v", err)
		return logs, err
	}
	defer conn.Close()

	addLog("TCP connection established")
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		addLog("SMTP client creation failed: %v", err)
		return logs, err
	}
	defer client.Close()

	addLog("SMTP server greeted successfully")
	if ok, _ := client.Extension("STARTTLS"); ok {
		addLog("Server supports STARTTLS; attempting upgrade")
		tlsConfig := &tls.Config{
			ServerName: c.Host,
			MinVersion: tls.VersionTLS12,
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			addLog("STARTTLS failed: %v", err)
			return logs, err
		}
		addLog("TLS negotiation succeeded")
	} else {
		addLog("Server does not advertise STARTTLS")
	}

	if c.Username != "" {
		if ok, mechanisms := client.Extension("AUTH"); ok {
			addLog("Server supports AUTH: %s", mechanisms)
		} else {
			addLog("Server does not advertise AUTH; attempting PlainAuth anyway")
		}

		auth := smtp.PlainAuth("", c.Username, c.Password, c.Host)
		addLog("Authenticating as %q", c.Username)
		if err := client.Auth(auth); err != nil {
			addLog("Authentication failed: %v", err)
			return logs, err
		}
		addLog("Authentication succeeded")
	} else {
		addLog("SMTP username is blank; skipping AUTH")
	}

	addLog("Issuing MAIL FROM <%s>", c.FromEmail)
	if err := client.Mail(c.FromEmail); err != nil {
		addLog("MAIL FROM failed: %v", err)
		return logs, err
	}

	addLog("Issuing RCPT TO <%s>", recipientEmail)
	if err := client.Rcpt(recipientEmail); err != nil {
		addLog("RCPT TO failed: %v", err)
		return logs, err
	}

	addLog("Opening DATA stream")
	writer, err := client.Data()
	if err != nil {
		addLog("DATA failed: %v", err)
		return logs, err
	}

	if _, err := writer.Write([]byte(body)); err != nil {
		addLog("Writing message body failed: %v", err)
		_ = writer.Close()
		return logs, err
	}
	if err := writer.Close(); err != nil {
		addLog("Closing DATA stream failed: %v", err)
		return logs, err
	}
	addLog("Message body accepted by server")

	if err := client.Quit(); err != nil {
		addLog("QUIT returned an error: %v", err)
		return logs, err
	}
	addLog("SMTP session completed successfully")

	return logs, nil
}

func (c ResendConfig) sendMailWithTrace(recipientEmail string, body string) ([]string, error) {
	logs := []string{}
	addLog := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf("%s %s", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...)))
	}

	if !c.isConfigured() {
		addLog("Resend config is incomplete: api_key_present=%t from_email=%q", c.APIKey != "", c.FromEmail)
		return logs, fmt.Errorf("email delivery is not configured")
	}

	subject := extractHeaderValue(body, "Subject")
	textBody := extractTextBody(body)
	if subject == "" {
		subject = "Blindspot email"
	}
	if textBody == "" {
		textBody = body
	}

	payload := map[string]any{
		"from":    c.FromEmail,
		"to":      []string{recipientEmail},
		"subject": subject,
		"text":    textBody,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		addLog("Failed to encode Resend payload: %v", err)
		return logs, err
	}

	addLog("Preparing Resend API request")
	addLog("POST https://api.resend.com/emails")
	addLog("From=%q To=%q Subject=%q", c.FromEmail, recipientEmail, subject)

	req, err := http.NewRequest(http.MethodPost, "https://api.resend.com/emails", strings.NewReader(string(payloadBytes)))
	if err != nil {
		addLog("Failed to build HTTP request: %v", err)
		return logs, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		addLog("HTTP request failed: %v", err)
		return logs, err
	}
	defer resp.Body.Close()

	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if readErr != nil {
		addLog("Failed to read Resend response body: %v", readErr)
		return logs, readErr
	}

	addLog("HTTP response status: %s", resp.Status)
	if len(responseBody) > 0 {
		addLog("Response body: %s", string(responseBody))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return logs, fmt.Errorf("resend API returned %s", resp.Status)
	}

	addLog("Resend accepted the message successfully")
	return logs, nil
}

func extractHeaderValue(message string, headerName string) string {
	for _, line := range strings.Split(message, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(headerName)+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, headerName+":"))
		}
	}
	return ""
}

func extractTextBody(message string) string {
	parts := strings.SplitN(message, "\r\n\r\n", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(message)
	}
	return strings.TrimSpace(parts[1])
}

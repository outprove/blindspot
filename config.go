package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

var appConfig = defaultAppConfig()

type AppConfig struct {
	SessionSecret string      `json:"session_secret"`
	SMTP          SMTPConfig  `json:"smtp"`
	Admin         AdminConfig `json:"admin"`
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

func defaultAppConfig() AppConfig {
	return AppConfig{
		SessionSecret: "dev-session-secret-change-me",
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

	cfg.SMTP.Host = strings.TrimSpace(cfg.SMTP.Host)
	cfg.SMTP.Port = strings.TrimSpace(cfg.SMTP.Port)
	cfg.SMTP.Username = strings.TrimSpace(cfg.SMTP.Username)
	cfg.SMTP.FromEmail = strings.TrimSpace(cfg.SMTP.FromEmail)
	if cfg.SMTP.Port == "" {
		cfg.SMTP.Port = "587"
	}

	cfg.Admin.Password = strings.TrimSpace(cfg.Admin.Password)

	applyEnvOverrides(&cfg)

	appConfig = cfg
	return cfg
}

func applyEnvOverrides(cfg *AppConfig) {
	if value := strings.TrimSpace(os.Getenv("SESSION_SECRET")); value != "" {
		cfg.SessionSecret = value
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

	if cfg.SMTP.Port == "" {
		cfg.SMTP.Port = "587"
	}

	// Railway exposes a PORT env var. We don't store it in config, but validating
	// it here makes startup issues easier to spot if someone reuses the value.
	if value := strings.TrimSpace(os.Getenv("PORT")); value != "" {
		if _, err := strconv.Atoi(value); err != nil {
			log.Printf("ignoring non-numeric PORT value %q", value)
		}
	}
}

func (c SMTPConfig) isConfigured() bool {
	return c.Host != "" && c.FromEmail != ""
}

func (c SMTPConfig) sendMail(recipientEmail string, body string) error {
	_, err := c.sendMailWithTrace(recipientEmail, body)
	return err
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

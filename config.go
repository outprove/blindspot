package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strconv"
	"strings"
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
	if !c.isConfigured() {
		return fmt.Errorf("email delivery is not configured")
	}

	auth := smtp.PlainAuth("", c.Username, c.Password, c.Host)
	return smtp.SendMail(c.Host+":"+c.Port, auth, c.FromEmail, []string{recipientEmail}, []byte(body))
}

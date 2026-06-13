package config

import (
	"log"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port        string
	DatabaseURL string
	SendGrid    SendGridConfig
	Twilio      TwilioConfig
	SuperTokens SuperTokensConfig
	Etcd        EtcdConfig
	Scheduler   SchedulerConfig
	Stripe      StripeConfig
}

type SchedulerConfig struct {
	InstanceID     string
	PartitionCount int
	SQLitePath     string
	LeaseTTL       time.Duration
	SyncInterval   time.Duration
	Etcd           EtcdConfig
}

type EtcdConfig struct {
	Endpoints   []string
	DialTimeout time.Duration
	Username    string
	Password    string
}

type SuperTokensConfig struct {
	ConnectionURI string
	APIKey        string
	APIDomain     string
	WebsiteDomain string
}

type SendGridConfig struct {
	APIKey   string
	From     string // verified sender email
	FromName string // display name, optional
}

type TwilioConfig struct {
	AccountSID string
	AuthToken  string
	FromNumber string
}

type StripeConfig struct {
	SecretKey         string
	WebhookSecret     string
	ProPriceID        string
	EnterprisePriceID string
}

func Load() *Config {
	cfg := &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgresql://postgres:postgres@localhost:5432/upguardly?sslmode=disable"),
		SuperTokens: SuperTokensConfig{
			ConnectionURI: getEnv("SUPERTOKENS_CONNECTION_URI", "http://localhost:3567"),
			APIKey:        getEnv("SUPERTOKENS_API_KEY", ""),
			APIDomain:     getEnv("API_DOMAIN", "http://localhost:8080"),
			WebsiteDomain: getEnv("WEBSITE_DOMAIN", "http://localhost:3000"),
		},
		SendGrid: SendGridConfig{
			APIKey:   getEnv("SENDGRID_API_KEY", ""),
			From:     getEnv("SENDGRID_FROM", ""),
			FromName: getEnv("SENDGRID_FROM_NAME", "Upguardly"),
		},
		Twilio: TwilioConfig{
			AccountSID: getEnv("TWILIO_SID", ""),
			AuthToken:  getEnv("TWILIO_TOKEN", ""),
			FromNumber: getEnv("TWILIO_FROM", ""),
		},
		Etcd: EtcdConfig{
			Endpoints:   []string{getEnv("ETCD_ENDPOINT", "http://localhost:2379")},
			DialTimeout: time.Duration(getEnvInt("ETCD_DIAL_TIMEOUT_SECONDS", 5)) * time.Second,
			Username:    getEnv("ETCD_USERNAME", ""),
			Password:    getEnv("ETCD_PASSWORD", ""),
		},
		Stripe: StripeConfig{
			SecretKey:         getEnv("STRIPE_SECRET_KEY", ""),
			WebhookSecret:     getEnv("STRIPE_WEBHOOK_SECRET", ""),
			ProPriceID:        getEnv("STRIPE_PRO_PRICE_ID", ""),
			EnterprisePriceID: getEnv("STRIPE_ENTERPRISE_PRICE_ID", ""),
		},
		Scheduler: SchedulerConfig{
			InstanceID:     getEnv("SCHEDULER_INSTANCE_ID", "scheduler-0"),
			PartitionCount: getEnvInt("SCHEDULER_PARTITION_COUNT", 1),
			SQLitePath:     getEnv("SCHEDULER_SQLITE_PATH", "/tmp/upguardly-scheduler.db"),
			LeaseTTL:       time.Duration(getEnvInt("SCHEDULER_LEASE_TTL_SECONDS", 30)) * time.Second,
			SyncInterval:   time.Duration(getEnvInt("SCHEDULER_SYNC_INTERVAL_SECONDS", 10)) * time.Second,
			Etcd: EtcdConfig{
				Endpoints:   []string{getEnv("ETCD_ENDPOINT", "http://localhost:2379")},
				DialTimeout: time.Duration(getEnvInt("ETCD_DIAL_TIMEOUT_SECONDS", 5)) * time.Second,
				Username:    getEnv("ETCD_USERNAME", ""),
				Password:    getEnv("ETCD_PASSWORD", ""),
			},
		},
	}

	cfg.warnMissingSecrets()
	return cfg
}

// warnMissingSecrets logs warnings for configuration that is required in
// production but not set. These are non-fatal at startup so that local
// development still works without every service configured, but the warnings
// make misconfigurations visible before they cause silent failures.
func (c *Config) warnMissingSecrets() {
	isLocalDefault := func(url string) bool {
		return url == "postgresql://postgres:postgres@localhost:5432/upguardly?sslmode=disable"
	}

	if isLocalDefault(c.DatabaseURL) {
		log.Println("[WARN] config: DATABASE_URL is using the insecure default — set DATABASE_URL in production")
	}
	if c.SuperTokens.APIKey == "" {
		log.Println("[WARN] config: SUPERTOKENS_API_KEY is not set — SuperTokens API may be unprotected")
	}
	if c.Stripe.SecretKey == "" {
		log.Println("[WARN] config: STRIPE_SECRET_KEY is not set — billing features will be unavailable")
	}
	if c.Stripe.WebhookSecret == "" {
		log.Println("[WARN] config: STRIPE_WEBHOOK_SECRET is not set — Stripe webhooks cannot be verified")
	}
	if c.SendGrid.APIKey == "" {
		log.Println("[WARN] config: SENDGRID_API_KEY is not set — email alerts and invitations will not be sent")
	}
	if os.Getenv("METRICS_TOKEN") == "" {
		log.Println("[WARN] config: METRICS_TOKEN is not set — /metrics endpoint is publicly accessible")
	}
	if c.SuperTokens.WebsiteDomain == "http://localhost:3000" && os.Getenv("WEBSITE_DOMAIN") == "" {
		log.Println("[WARN] config: WEBSITE_DOMAIN is not set — invitation emails will link to localhost")
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultValue
}

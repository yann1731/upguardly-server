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
	Redis       RedisConfig
	RateLimit   RateLimitConfig
}

type RedisConfig struct {
	// URL is a standard redis connection string (redis://[:password@]host:port/db).
	// When empty, Redis-backed features (e.g. distributed rate limiting) fall
	// back to a safe local default and the limiter fails open.
	URL string
}

type RateLimitConfig struct {
	// DefaultPerMin is the global per-IP request budget per window applied to
	// every route. StrictPerMin is the tighter budget for mutation endpoints.
	// Both are tunable so operators can retune without a rebuild.
	DefaultPerMin int
	StrictPerMin  int
	// Window is the fixed window over which the budgets are counted.
	Window time.Duration
	// RequireRedis makes a missing/unreachable Redis fatal at startup instead of
	// silently falling back to per-process in-memory counters. Must be true for
	// any multi-replica deployment, otherwise the global limit is not enforced.
	RequireRedis bool
}

type SchedulerConfig struct {
	// Embedded controls whether the API server (cmd/server) runs an in-process
	// scheduler that checks all monitors. It must be false whenever a dedicated
	// scheduler binary (cmd/scheduler) is running or the API server is scaled to
	// more than one replica, otherwise monitors are checked — and alerts fired —
	// multiple times. Default false; enable only for single-box deployments.
	Embedded       bool
	InstanceID     string
	PartitionCount int
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
	// Enabled gates ALL outbound email (alerts, invitations, password reset,
	// verification). When false, sends become no-ops that log what would have
	// been sent — set EMAIL_ENABLED=false in development and load tests so
	// they can't burn SendGrid API quota. Default true.
	Enabled  bool
	APIKey   string
	From     string // verified sender email
	FromName string // display name, optional
}

type TwilioConfig struct {
	AccountSID   string // Account SID (AC…), used in the request URL
	APIKeySID    string // API Key SID (SK…), used as the Basic-auth username
	APIKeySecret string // API Key secret, used as the Basic-auth password
	FromNumber   string
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
			Enabled:  getEnvBool("EMAIL_ENABLED", true),
			APIKey:   getEnv("SENDGRID_API_KEY", ""),
			From:     getEnv("SENDGRID_FROM", ""),
			FromName: getEnv("SENDGRID_FROM_NAME", "Upguardly"),
		},
		Twilio: TwilioConfig{
			AccountSID:   getEnv("TWILIO_SID", ""),
			APIKeySID:    getEnv("TWILIO_API_KEY_SID", ""),
			APIKeySecret: getEnv("TWILIO_API_KEY_SECRET", ""),
			FromNumber:   getEnv("TWILIO_FROM", ""),
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
		Redis: RedisConfig{
			URL: getEnv("REDIS_URL", ""),
		},
		RateLimit: RateLimitConfig{
			DefaultPerMin: getEnvInt("RATE_LIMIT_DEFAULT_PER_MIN", 50000),
			StrictPerMin:  getEnvInt("RATE_LIMIT_STRICT_PER_MIN", 1200),
			Window:        time.Duration(getEnvInt("RATE_LIMIT_WINDOW_SECONDS", 60)) * time.Second,
			RequireRedis:  getEnvBool("RATE_LIMIT_REQUIRE_REDIS", false),
		},
		Scheduler: SchedulerConfig{
			Embedded:   getEnvBool("EMBEDDED_SCHEDULER", false),
			InstanceID: getEnv("SCHEDULER_INSTANCE_ID", "scheduler-0"),
			// Default matches the production compose file. With a count of 1
			// only one instance can ever own work, so scaling out adds zero
			// capacity — keep this well above the expected instance count.
			PartitionCount: getEnvInt("SCHEDULER_PARTITION_COUNT", 64),
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
	if !c.SendGrid.Enabled {
		log.Println("[INFO] config: EMAIL_ENABLED=false — all outbound email is disabled (dry-run logs only)")
	} else if c.SendGrid.APIKey == "" {
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

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return defaultValue
}

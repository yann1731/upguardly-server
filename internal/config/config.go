package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Config struct {
	Port         string
	DatabaseURL  string
	SMTP         SMTPConfig
	Twilio       TwilioConfig
	Scheduler    SchedulerConfig
	SuperTokens  SuperTokensConfig
}

type SuperTokensConfig struct {
	ConnectionURI string
	APIKey        string
	AppName       string
	APIDomain     string
	WebsiteDomain string
}

type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

type TwilioConfig struct {
	AccountSID string
	AuthToken  string
	FromNumber string
}

type SchedulerConfig struct {
	InstanceID     string
	PartitionCount int
	SyncInterval   time.Duration
	LeaseTTL       time.Duration
	SQLitePath     string
	Etcd           EtcdConfig
}

type EtcdConfig struct {
	Endpoints   []string
	DialTimeout time.Duration
	Username    string
	Password    string
}

func Load() *Config {
	instanceID := getEnv("SCHEDULER_INSTANCE_ID", "")
	if instanceID == "" {
		instanceID = "sched-" + uuid.New().String()[:8]
	}

	return &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgresql://postgres:postgres@localhost:5432/upguardly?sslmode=disable"),
		SMTP: SMTPConfig{
			Host:     getEnv("SMTP_HOST", ""),
			Port:     getEnvInt("SMTP_PORT", 587),
			User:     getEnv("SMTP_USER", ""),
			Password: getEnv("SMTP_PASS", ""),
			From:     getEnv("SMTP_FROM", ""),
		},
		Twilio: TwilioConfig{
			AccountSID: getEnv("TWILIO_SID", ""),
			AuthToken:  getEnv("TWILIO_TOKEN", ""),
			FromNumber: getEnv("TWILIO_FROM", ""),
		},
		Scheduler: SchedulerConfig{
			InstanceID:     instanceID,
			PartitionCount: getEnvInt("SCHEDULER_PARTITION_COUNT", 64),
			SyncInterval:   getEnvDuration("SCHEDULER_SYNC_INTERVAL", 10*time.Second),
			LeaseTTL:       getEnvDuration("SCHEDULER_LEASE_TTL", 10*time.Second),
			SQLitePath:     getEnv("SQLITE_PATH", "./scheduler.db"),
			Etcd: EtcdConfig{
				Endpoints:   getEnvStringSlice("ETCD_ENDPOINTS", []string{"localhost:2379"}),
				DialTimeout: getEnvDuration("ETCD_DIAL_TIMEOUT", 5*time.Second),
				Username:    getEnv("ETCD_USERNAME", ""),
				Password:    getEnv("ETCD_PASSWORD", ""),
			},
		},
		SuperTokens: SuperTokensConfig{
			ConnectionURI: getEnv("SUPERTOKENS_CONNECTION_URI", "http://localhost:3567"),
			APIKey:        getEnv("SUPERTOKENS_API_KEY", ""),
			AppName:       getEnv("SUPERTOKENS_APP_NAME", "Upguardly"),
			APIDomain:     getEnv("SUPERTOKENS_API_DOMAIN", "http://localhost:8080"),
			WebsiteDomain: getEnv("SUPERTOKENS_WEBSITE_DOMAIN", "http://localhost:3000"),
		},
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

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

func getEnvStringSlice(key string, defaultValue []string) []string {
	if value := os.Getenv(key); value != "" {
		parts := strings.Split(value, ",")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return defaultValue
}

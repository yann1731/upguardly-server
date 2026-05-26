package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port        string
	DatabaseURL string
	SMTP        SMTPConfig
	Twilio      TwilioConfig
	SuperTokens SuperTokensConfig
	Etcd        EtcdConfig
	Scheduler   SchedulerConfig
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

func Load() *Config {
	return &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgresql://postgres:postgres@localhost:5432/upguardly?sslmode=disable"),
		SuperTokens: SuperTokensConfig{
			ConnectionURI: getEnv("SUPERTOKENS_CONNECTION_URI", "http://localhost:3567"),
			APIKey:        getEnv("SUPERTOKENS_API_KEY", ""),
			APIDomain:     getEnv("API_DOMAIN", "http://localhost:8080"),
			WebsiteDomain: getEnv("WEBSITE_DOMAIN", "http://localhost:3000"),
		},
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
		Etcd: EtcdConfig{
			Endpoints:   []string{getEnv("ETCD_ENDPOINT", "http://localhost:2379")},
			DialTimeout: time.Duration(getEnvInt("ETCD_DIAL_TIMEOUT_SECONDS", 5)) * time.Second,
			Username:    getEnv("ETCD_USERNAME", ""),
			Password:    getEnv("ETCD_PASSWORD", ""),
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

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/api"
	"upguardly-backend/internal/api/middleware"
	"upguardly-backend/internal/auth"
	"upguardly-backend/internal/config"
	"upguardly-backend/internal/database"
	"upguardly-backend/internal/mailer"
	"upguardly-backend/internal/redisclient"
	"upguardly-backend/internal/scheduler"
	"upguardly-backend/internal/stripeservice"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("[INFO] no .env file loaded (%v); relying on process environment", err)
	}

	cfg := config.Load()

	m := mailer.NewMailer(cfg.SendGrid)

	if err := auth.Init(cfg, m); err != nil {
		log.Fatalf("Failed to initialize SuperTokens: %v", err)
	}
	log.Println("SuperTokens initialized")

	db := database.NewClient()
	if err := db.Connect(); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Disconnect()

	log.Println("Connected to database")

	// Configure the rate limiters from env before the router serves traffic.
	middleware.InitRateLimiters(cfg.RateLimit.DefaultPerMin, cfg.RateLimit.StrictPerMin, cfg.RateLimit.Window)

	// Shared Redis client backs distributed rate limiting (and, later, caching).
	// When REDIS_URL is unset the limiter falls back to per-process in-memory
	// counters — correct only for a single API replica. Any multi-replica
	// deployment MUST set RATE_LIMIT_REQUIRE_REDIS=true so a missing/unreachable
	// Redis is fatal here rather than silently un-enforcing the global limit.
	rdb, err := redisclient.New(cfg.Redis.URL)
	switch {
	case err != nil:
		if cfg.RateLimit.RequireRedis {
			log.Fatalf("RATE_LIMIT_REQUIRE_REDIS is set but Redis is unavailable: %v", err)
		}
		log.Printf("[WARN] redis unavailable (%v); rate limiting falls back to in-memory (single-instance only)", err)
	case rdb != nil:
		middleware.SetRedisClient(rdb)
		defer rdb.Close()
		log.Println("Connected to Redis — distributed rate limiting enabled")
	default:
		if cfg.RateLimit.RequireRedis {
			log.Fatalf("RATE_LIMIT_REQUIRE_REDIS is set but REDIS_URL is empty; refusing to run with per-process rate limiting")
		}
		log.Println("[WARN] REDIS_URL not set; rate limiting is in-memory (single-instance only)")
	}

	alertManager := alerter.NewManager(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The embedded scheduler checks every monitor in-process. It is intended
	// only for single-box deployments. When the API server is scaled to more
	// than one replica, or a dedicated cmd/scheduler is running, this MUST be
	// disabled (EMBEDDED_SCHEDULER=false) to avoid duplicate checks and alerts.
	var sched *scheduler.Scheduler
	if cfg.Scheduler.Embedded {
		sched = scheduler.NewScheduler(db, alertManager)
		if err := sched.Start(ctx); err != nil {
			log.Fatalf("Failed to start scheduler: %v", err)
		}
		log.Println("Embedded scheduler started (single-box mode)")
	} else {
		log.Println("Embedded scheduler disabled — relying on dedicated scheduler instance(s)")
	}

	store := database.NewPrismaStore(db)
	s := stripeservice.NewClient(cfg.Stripe)
	router := api.NewRouter(store, cfg.SuperTokens.WebsiteDomain, m, s)

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	go func() {
		log.Printf("Server starting on port %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	cancel()
	if sched != nil {
		sched.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

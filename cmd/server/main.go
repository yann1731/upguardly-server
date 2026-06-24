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
	"upguardly-backend/internal/auth"
	"upguardly-backend/internal/config"
	"upguardly-backend/internal/database"
	"upguardly-backend/internal/mailer"
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

	alertManager := alerter.NewManager(cfg)

	sched := scheduler.NewScheduler(db, alertManager)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		log.Fatalf("Failed to start scheduler: %v", err)
	}
	log.Println("Scheduler started")

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
	sched.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

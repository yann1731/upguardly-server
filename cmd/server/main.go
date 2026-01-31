package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/api"
	"upguardly-backend/internal/config"
	"upguardly-backend/internal/database"
	"upguardly-backend/internal/scheduler"
)

func main() {
	cfg := config.Load()

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

	router := api.NewRouter(db)

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

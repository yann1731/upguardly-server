package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"upguardly-backend/internal/api"
	"upguardly-backend/internal/auth"
	"upguardly-backend/internal/config"
	"upguardly-backend/internal/database"
)

func main() {
	cfg := config.Load()

	if err := auth.Init(cfg.SuperTokens); err != nil {
		log.Fatalf("Failed to initialize SuperTokens: %v", err)
	}
	log.Println("SuperTokens initialized")

	db := database.NewClient()
	if err := db.Connect(); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Disconnect()

	log.Println("Connected to database")

	router := api.NewRouter(db)

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	go func() {
		log.Printf("API server starting on port %s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

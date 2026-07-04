package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/config"
	"upguardly-backend/internal/coordination"
	bundb "upguardly-backend/internal/database/bun"
	"upguardly-backend/internal/models"
	"upguardly-backend/internal/scheduler"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("[INFO] no .env file loaded (%v); relying on process environment", err)
	}

	cfg := config.Load()

	if !models.ValidRegion(cfg.Scheduler.Region) {
		log.Fatalf("Invalid SCHEDULER_REGION %q (known regions: %v)", cfg.Scheduler.Region, models.RegionIDs())
	}

	log.Printf("Starting scheduler instance: %s", cfg.Scheduler.InstanceID)
	log.Printf("Region: %s", cfg.Scheduler.Region)
	log.Printf("Partition count: %d", cfg.Scheduler.PartitionCount)
	log.Printf("etcd endpoints: %v", cfg.Scheduler.Etcd.Endpoints)

	db := bundb.NewClient(cfg.DatabaseURL)
	if err := db.Connect(); err != nil {
		log.Fatalf("Failed to connect to Bun database: %v", err)
	}
	defer db.Disconnect()
	log.Println("Connected to PostgreSQL database (via Bun)")

	store := bundb.NewBunStore(db)

	coordinator, err := coordination.NewCoordinator(
		cfg.Scheduler.Etcd,
		cfg.Scheduler.InstanceID,
		cfg.Scheduler.Region,
		cfg.Scheduler.LeaseTTL,
	)
	if err != nil {
		log.Fatalf("Failed to create etcd coordinator: %v", err)
	}
	defer coordinator.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := coordinator.Register(ctx); err != nil {
		log.Fatalf("Failed to register with etcd: %v", err)
	}

	partitions := coordination.NewPartitionManager(
		cfg.Scheduler.PartitionCount,
		cfg.Scheduler.InstanceID,
	)

	alertManager := alerter.NewManager(cfg)

	sched := scheduler.NewDistributedScheduler(
		store,
		alertManager,
		coordinator,
		partitions,
		cfg.Scheduler.SyncInterval,
		cfg.Scheduler.Region,
	)

	if err := sched.Start(ctx); err != nil {
		log.Fatalf("Failed to start distributed scheduler: %v", err)
	}
	log.Println("Distributed scheduler started")

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down scheduler...")

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := coordinator.Deregister(shutdownCtx); err != nil {
		log.Printf("Warning: failed to deregister from etcd: %v", err)
	}

	sched.Stop()

	log.Println("Scheduler exited")
}

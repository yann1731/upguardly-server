package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"upguardly-backend/internal/alerter"
	"upguardly-backend/internal/config"
	"upguardly-backend/internal/coordination"
	"upguardly-backend/internal/database"
	"upguardly-backend/internal/scheduler"
	"upguardly-backend/internal/statestore"
)

func main() {
	cfg := config.Load()

	log.Printf("Starting scheduler instance: %s", cfg.Scheduler.InstanceID)
	log.Printf("Partition count: %d", cfg.Scheduler.PartitionCount)
	log.Printf("etcd endpoints: %v", cfg.Scheduler.Etcd.Endpoints)

	db := database.NewClient()
	if err := db.Connect(); err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Disconnect()
	log.Println("Connected to PostgreSQL database")

	stateStore, err := statestore.NewSQLiteStore(cfg.Scheduler.SQLitePath)
	if err != nil {
		log.Fatalf("Failed to initialize SQLite state store: %v", err)
	}
	defer stateStore.Close()
	log.Printf("Initialized SQLite state store at: %s", cfg.Scheduler.SQLitePath)

	coordinator, err := coordination.NewCoordinator(
		cfg.Scheduler.Etcd,
		cfg.Scheduler.InstanceID,
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
		db,
		alertManager,
		coordinator,
		partitions,
		stateStore,
		cfg.Scheduler.SyncInterval,
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

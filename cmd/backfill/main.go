// Command backfill re-snaps existing monitors to their owner's current plan
// limits. It exists to repair accounts whose plan-change webhook ran while
// ReconcileMonitorsToPlan had a mis-ordered SQL argument list: the reconcile
// UPDATE errored and was swallowed, so monitors created on a lower tier stayed
// pinned to that tier's check interval (and region cap) after an upgrade.
//
// For every subscription whose effective plan is not FREE it replays the
// upgrade reconcile with FREE as the synthetic previous plan
// (store.ReconcileMonitorsToPlan(userID, "FREE", plan)). That reproduces exactly
// the snap the webhook should have performed and is a no-op for monitors already
// at or below their plan floor, so the command is idempotent and safe to re-run.
//
// It is dry-run by default; pass -confirm to write.
//
//	go run ./cmd/backfill            # list accounts that would be adjusted
//	go run ./cmd/backfill -confirm   # apply
package main

import (
	"context"
	"flag"
	"log"

	"github.com/joho/godotenv"

	"upguardly-backend/internal/config"
	bundb "upguardly-backend/internal/database/bun"
)

func main() {
	confirm := flag.Bool("confirm", false, "apply changes (otherwise dry-run: only report eligible accounts)")
	flag.Parse()

	if err := godotenv.Load(); err != nil {
		log.Printf("[INFO] no .env file loaded (%v); relying on process environment", err)
	}

	cfg := config.Load()

	db := bundb.NewClient(cfg.DatabaseURL)
	if err := db.Connect(); err != nil {
		log.Fatalf("Failed to connect to Bun database: %v", err)
	}
	defer db.Disconnect()

	store := bundb.NewBunStore(db)
	ctx := context.Background()

	var subs []bundb.Subscription
	if err := db.DB.NewSelect().Model(&subs).Scan(ctx); err != nil {
		log.Fatalf("Failed to list subscriptions: %v", err)
	}

	if !*confirm {
		log.Printf("[DRY-RUN] no changes will be written; pass -confirm to apply")
	}

	var eligible, adjusted int
	for i := range subs {
		s := subs[i]
		plan := effectivePlan(s.Plan, s.Status)
		if plan == "FREE" {
			continue
		}
		eligible++
		if !*confirm {
			log.Printf("[DRY-RUN] would reconcile user %s to plan %s", s.UserID, plan)
			continue
		}
		n, err := store.ReconcileMonitorsToPlan(ctx, s.UserID, "FREE", plan)
		if err != nil {
			log.Printf("[ERROR] user %s (%s): reconcile failed: %v", s.UserID, plan, err)
			continue
		}
		if n > 0 {
			log.Printf("user %s -> %s: adjusted %d monitor(s)", s.UserID, plan, n)
		}
		adjusted += n
	}

	if *confirm {
		log.Printf("Backfill complete: %d non-FREE account(s), %d monitor(s) adjusted", eligible, adjusted)
	} else {
		log.Printf("[DRY-RUN] %d non-FREE account(s) would be reconciled; re-run with -confirm to apply", eligible)
	}
}

// effectivePlan mirrors handlers.effectivePlan: CANCELED (and the terminal
// statuses that map to it) carries no entitlement and resolves to FREE.
func effectivePlan(plan, status string) string {
	if status == "CANCELED" {
		return "FREE"
	}
	return plan
}

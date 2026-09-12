package bun

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"upguardly-backend/internal/models"
)

// UpsertSubscription's ON CONFLICT clause COALESCEs the nullable Stripe columns
// against the pre-update row so partial writes don't erase them, and it spells
// that row `"s"."col"` — which only works because bun renders the model's alias
// into the INSERT target. Pin that: if a bun upgrade stopped emitting `AS "s"`,
// the upsert would fail at runtime on a column that does not exist, and only
// when a conflict actually fires.
func TestSubscriptionInsertCarriesTableAlias(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	// bun only aliases the insert target when there is an ON CONFLICT clause,
	// so build the same shape UpsertSubscription does.
	sql := db.NewInsert().Model(&Subscription{}).
		On("CONFLICT (user_id) DO UPDATE").
		Set(`stripe_customer_id = COALESCE(EXCLUDED.stripe_customer_id, "s"."stripe_customer_id")`).
		String()

	if !strings.Contains(sql, `INSERT INTO "subscriptions" AS "s"`) {
		t.Fatalf(`expected the insert to alias the table as "s", got: %s`, sql)
	}
}

// ── DB-backed: the ON CONFLICT clause ─────────────────────────────────────────
//
// UpsertSubscription's conflict clause is raw SQL that the mockStore-based
// handler tests cannot exercise. Two behaviours depend on it and both were
// wrong or untested before: cancel_at_period_end must round-trip, and a caller
// that supplies only user/plan/status must not erase the Stripe IDs and period
// dates (handleSubscriptionDeleted and handlePaymentFailed do exactly that).
//
// Needs a real Postgres with migrations applied; skipped otherwise — see
// store_reconcile_test.go for the setup.

func TestUpsertSubscriptionConflictClause(t *testing.T) {
	store := reconcileTestStore(t)
	ctx := context.Background()
	userID := "upsert-test-" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = store.client.DB.NewDelete().Model((*Subscription)(nil)).Where("user_id = ?", userID).Exec(ctx)
	})

	cust, subID, price := "cus_"+userID, "sub_"+userID, "price_pro"
	start := time.Now().Add(-24 * time.Hour).Truncate(time.Millisecond)
	end := time.Now().Add(24 * time.Hour).Truncate(time.Millisecond)

	full := models.UpsertSubscriptionParams{
		UserID:               userID,
		Plan:                 "PRO",
		Status:               "ACTIVE",
		StripeCustomerID:     &cust,
		StripeSubscriptionID: &subID,
		StripePriceID:        &price,
		CurrentPeriodStart:   &start,
		CurrentPeriodEnd:     &end,
	}
	if _, err := store.UpsertSubscription(ctx, full); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}

	t.Run("cancel_at_period_end round-trips", func(t *testing.T) {
		canceling := full
		canceling.CancelAtPeriodEnd = true
		if _, err := store.UpsertSubscription(ctx, canceling); err != nil {
			t.Fatalf("upsert: %v", err)
		}

		got, err := store.GetSubscriptionByUser(ctx, userID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if !got.CancelAtPeriodEnd {
			t.Error("cancelAtPeriodEnd was not persisted")
		}
		if got.Status != "ACTIVE" {
			t.Errorf("status = %q, want ACTIVE (grace runs to the period end)", got.Status)
		}
	})

	t.Run("a partial write preserves the Stripe ids and period dates", func(t *testing.T) {
		// Exactly what handleSubscriptionDeleted sends.
		if _, err := store.UpsertSubscription(ctx, models.UpsertSubscriptionParams{
			UserID: userID,
			Plan:   "FREE",
			Status: "CANCELED",
		}); err != nil {
			t.Fatalf("partial upsert: %v", err)
		}

		got, err := store.GetSubscriptionByUser(ctx, userID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}

		if got.Plan != "FREE" || got.Status != "CANCELED" {
			t.Errorf("plan/status = %q/%q, want FREE/CANCELED", got.Plan, got.Status)
		}
		// Losing these 404s the portal and cancel endpoints and forces reconcile
		// back into a Stripe customer search.
		if got.StripeCustomerID == nil || *got.StripeCustomerID != cust {
			t.Errorf("stripe_customer_id = %v, want preserved %q", got.StripeCustomerID, cust)
		}
		if got.StripeSubscriptionID == nil || *got.StripeSubscriptionID != subID {
			t.Errorf("stripe_subscription_id = %v, want preserved %q", got.StripeSubscriptionID, subID)
		}
		if got.StripePriceID == nil || *got.StripePriceID != price {
			t.Errorf("stripe_price_id = %v, want preserved %q", got.StripePriceID, price)
		}
		if got.CurrentPeriodEnd == nil || !got.CurrentPeriodEnd.Equal(end) {
			t.Errorf("current_period_end = %v, want preserved %v", got.CurrentPeriodEnd, end)
		}
		if got.CurrentPeriodStart == nil || !got.CurrentPeriodStart.Equal(start) {
			t.Errorf("current_period_start = %v, want preserved %v", got.CurrentPeriodStart, start)
		}
		// The cancellation is done, not pending: this one does overwrite.
		if got.CancelAtPeriodEnd {
			t.Error("cancelAtPeriodEnd should be cleared once the period has ended")
		}
	})
}

-- Persist Stripe's cancel_at_period_end flag.
--
-- It used to be derived from live Stripe state on read and never stored
-- (models.Subscription.CancelAtPeriodEnd), but that read is throttled to once
-- per user per minute per process (handlers.reconcileTTL). Every request served
-- from the row itself therefore reported false, and the billing page rendered a
-- scheduled-to-cancel plan as "renews on <period end>".
--
-- Deliberately not part of SubscriptionStatus: a subscription scheduled to
-- cancel is still `active` at Stripe and keeps its entitlement until the period
-- lapses, so status stays ACTIVE and effectivePlan is unchanged. This column
-- only says that no renewal is coming.
--
-- Existing rows default to false; the first webhook or reconcile corrects
-- anyone who actually has a cancellation pending, so no backfill is needed.
ALTER TABLE "subscriptions"
    ADD COLUMN "cancel_at_period_end" BOOLEAN NOT NULL DEFAULT false;

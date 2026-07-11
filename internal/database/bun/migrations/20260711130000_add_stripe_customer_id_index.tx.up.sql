-- Add a partial unique index on stripe_customer_id in subscriptions.
-- Since a Stripe customer ID maps to at most one user/subscription,
-- this enforces uniqueness while allowing multiple NULL values for free tier users.
CREATE UNIQUE INDEX "subscriptions_stripe_customer_id_key"
ON "subscriptions"("stripe_customer_id")
WHERE "stripe_customer_id" IS NOT NULL;

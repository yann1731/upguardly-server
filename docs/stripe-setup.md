# Stripe Billing Setup

Billing is fully implemented in the backend (`internal/stripeservice/client.go`,
`internal/api/handlers/subscriptions.go`) and frontend (`app/billing/`,
`SubscriptionCard.tsx`). It is **dormant until four environment variables are set**. With
them unset the server boots fine but logs warnings (`internal/config/config.go`) and
billing endpoints return errors.

This guide walks through obtaining the values for the placeholders in:

- `upguardly-server/.env` (local dev — leave blank unless testing)
- `deploy/backend-server/.env.staging` (Stripe **test** mode)
- `deploy/backend-server/.env.production` (Stripe **live** mode)

| Variable | Format | Purpose |
| --- | --- | --- |
| `STRIPE_SECRET_KEY` | `sk_test_…` / `sk_live_…` | Authenticates API calls (checkout, portal, customers). |
| `STRIPE_WEBHOOK_SECRET` | `whsec_…` | Verifies signatures on `POST /v1/webhooks/stripe`. |
| `STRIPE_PRO_PRICE_ID` | `price_…` | Recurring price for the PRO plan. |
| `STRIPE_ENTERPRISE_PRICE_ID` | `price_…` | Recurring price for the ENTERPRISE plan. |

Use Stripe **test mode** for local + staging and **live mode** for production. Keys and
price IDs are mode-specific and not interchangeable.

## 1. Create products and prices

The app sells two paid plans (FREE is the default and needs no Stripe object). The plan
limits live in `internal/models/plan.go`:

| Plan | Max monitors | Max alerts / monitor |
| --- | --- | --- |
| FREE | 5 | 1 |
| PRO | 50 | 10 |
| ENTERPRISE | unlimited | unlimited |

In the Stripe dashboard → **Product catalog** → **Add product**:

1. Create a product **Upguardly PRO** with a recurring price → copy its `price_…` ID into
   `STRIPE_PRO_PRICE_ID`.
2. Create a product **Upguardly ENTERPRISE** with a recurring price → copy its `price_…` ID
   into `STRIPE_ENTERPRISE_PRICE_ID`.

`PriceIDForPlan` in `internal/stripeservice/client.go` maps the plan name a customer
selects to these IDs, so they must be set for checkout to work.

## 2. Copy the secret key

Dashboard → **Developers → API keys** → copy the **Secret key** into `STRIPE_SECRET_KEY`
(`sk_test_…` for staging, `sk_live_…` for production).

## 3. Configure the webhook

Dashboard → **Developers → Webhooks → Add endpoint**:

- **Endpoint URL:** `https://<api-domain>/v1/webhooks/stripe`
  (e.g. `https://api-staging.upguardly.com/v1/webhooks/stripe`,
  `https://api.upguardly.com/v1/webhooks/stripe`).
- **Events to send** — the handler in `subscriptions.go` only processes these, so subscribe
  to exactly:
  - `customer.subscription.created`
  - `customer.subscription.updated`
  - `customer.subscription.deleted`
  - `invoice.payment_failed`
- After creating, copy the endpoint's **Signing secret** (`whsec_…`) into
  `STRIPE_WEBHOOK_SECRET`.

## 4. Local testing

You don't need a public URL locally — use the Stripe CLI:

```bash
stripe login
stripe listen --forward-to localhost:8080/v1/webhooks/stripe
```

`stripe listen` prints a `whsec_…` secret; put it in `upguardly-server/.env` as
`STRIPE_WEBHOOK_SECRET`, set `STRIPE_SECRET_KEY` to your `sk_test_…` key, and set the two
test-mode price IDs. Trigger events with e.g. `stripe trigger customer.subscription.updated`.

## 5. Verify

1. Restart the server. The startup warnings from `config.go`
   (`STRIPE_SECRET_KEY is not set`, `STRIPE_WEBHOOK_SECRET is not set`) should no longer
   appear.
2. From the billing UI, start a PRO upgrade → you should be redirected to Stripe Checkout.
3. Complete a test payment → the `customer.subscription.created`/`updated` webhook should
   upsert the `Subscription` row (plan, status, `currentPeriodEnd`).
4. Use the **Manage billing** button to confirm the billing-portal session opens.

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
| `STRIPE_SECRET_KEY` | `rk_test_…` / `rk_live_…` | Authenticates API calls (checkout, portal, customers). Use a **restricted** key — see §2. |
| `STRIPE_WEBHOOK_SECRET` | `whsec_…` | Verifies signatures on `POST /v1/webhooks/stripe`. |
| `STRIPE_PRO_PRICE_ID` | `price_…` | Recurring price for the PRO plan. |
| `STRIPE_ENTERPRISE_PRICE_ID` | `price_…` | Recurring price for the ENTERPRISE plan. |

Use Stripe **test mode** for local + staging and **live mode** for production. Keys and
price IDs are mode-specific and not interchangeable.

## 1. Create products and prices

The app sells two paid plans (FREE is the default and needs no Stripe object). The plan
limits live in `internal/models/plan.go`:

| Plan | Max monitors | Max integrations |
| --- | --- | --- |
| FREE | 5 | 3 |
| PRO | 20 | 10 |
| ENTERPRISE | 200 | unlimited |

In the Stripe dashboard → **Product catalog** → **Add product**:

1. Create a product **Upguardly PRO** with a recurring price → copy its `price_…` ID into
   `STRIPE_PRO_PRICE_ID`.
2. Create a product **Upguardly ENTERPRISE** with a recurring price → copy its `price_…` ID
   into `STRIPE_ENTERPRISE_PRICE_ID`.

`PriceIDForPlan` in `internal/stripeservice/client.go` maps the plan name a customer
selects to these IDs, so they must be set for checkout to work.

## 2. Create a restricted key

Don't use the full-access **Secret key** — a leaked `sk_…` key can do anything on the Stripe
account. The backend only touches customers, checkout sessions, billing-portal sessions and
subscriptions (webhooks are verified with the signing secret, not the API key), so create a
**restricted key** scoped to exactly those.

Dashboard → **Developers → API keys** → **Create restricted key**. Set these permissions and
leave **everything else as None**:

| Resource | Permission |
| --- | --- |
| Customers | **Write** (Write includes Read) |
| Checkout Sessions | **Write** |
| Customer portal | **Write** |
| Subscriptions | **Write** |

**Subscriptions** is easy to miss and fails quietly. Reads need it
(`GetActiveSubscription` lists the customer's subscriptions to reconcile the stored record
against live Stripe state) and so does cancel/resume (`SetCancelAtPeriodEnd` updates the
subscription). Without it every reconcile fails and the billing page serves a stale record
indefinitely — which looks exactly like a sync bug, not a permissions one. Check the server log
for a `permission`/`401` from Stripe before chasing anything else.

Copy the resulting key into `STRIPE_SECRET_KEY` (`rk_test_…` for local/staging, `rk_live_…`
for production). If you later add Stripe API calls to the backend, this key must be reissued
with the matching scope or those calls will fail with a `permission`/`401` error.

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
  - `customer.subscription.paused`
  - `customer.subscription.resumed`
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
`STRIPE_WEBHOOK_SECRET`, set `STRIPE_SECRET_KEY` to your restricted `rk_test_…` key, and set the two
test-mode price IDs. Trigger events with e.g. `stripe trigger customer.subscription.updated`.

## 5. Verify

1. Restart the server. The startup warnings from `config.go`
   (`STRIPE_SECRET_KEY is not set`, `STRIPE_WEBHOOK_SECRET is not set`) should no longer
   appear.
2. From the billing UI, start a PRO upgrade → you should be redirected to Stripe Checkout.
3. Complete a test payment → the `customer.subscription.created`/`updated` webhook should
   upsert the `Subscription` row (plan, status, `currentPeriodEnd`).
4. Use the **Manage billing** button to confirm the billing-portal session opens.

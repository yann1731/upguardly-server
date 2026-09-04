package stripeservice

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/stripe/stripe-go/v85"
	portalsession "github.com/stripe/stripe-go/v85/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v85/checkout/session"
	"github.com/stripe/stripe-go/v85/customer"
	"github.com/stripe/stripe-go/v85/subscription"
	"github.com/stripe/stripe-go/v85/webhook"

	"upguardly-backend/internal/config"
)

type Client struct {
	cfg config.StripeConfig
	// warnedVersions dedupes the API-version drift warning in ParseWebhook so
	// a mismatched account logs once per version rather than once per event.
	warnedVersions sync.Map
}

func NewClient(cfg config.StripeConfig) *Client {
	stripe.Key = cfg.SecretKey
	return &Client{cfg: cfg}
}

// PriceIDForPlan returns the Stripe price ID for the given plan name.
func (c *Client) PriceIDForPlan(plan string) (string, error) {
	switch plan {
	case "PRO":
		return c.cfg.ProPriceID, nil
	case "ENTERPRISE":
		return c.cfg.EnterprisePriceID, nil
	default:
		return "", fmt.Errorf("unknown plan: %s", plan)
	}
}

// EnsureCustomer looks up or creates the Stripe customer for a user (the
// billing subject), keyed on user_id metadata. The metadata is what the
// subscription webhooks resolve the subscription back to.
func (c *Client) EnsureCustomer(userID, email string) (string, error) {
	if id, err := c.FindCustomerByUser(userID); err != nil {
		return "", err
	} else if id != "" {
		return id, nil
	}

	params := &stripe.CustomerParams{}
	if email != "" {
		params.Email = stripe.String(email)
	}
	params.AddMetadata("user_id", userID)

	cust, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("failed to create Stripe customer: %w", err)
	}
	return cust.ID, nil
}

// FindCustomerByUser returns the Stripe customer ID whose user_id metadata
// matches userID, or "" if none exists. It never creates a customer.
//
// This uses the Customer Search API (one indexed query) rather than listing
// every customer and scanning metadata client-side, which was O(all
// customers) in paginated API calls per lookup. Search is eventually
// consistent (up to ~1 minute); callers that just created a customer must
// hold on to the returned ID rather than relying on an immediate re-search.
func (c *Client) FindCustomerByUser(userID string) (string, error) {
	params := &stripe.CustomerSearchParams{
		SearchParams: stripe.SearchParams{
			Query: fmt.Sprintf("metadata['user_id']:'%s'", strings.ReplaceAll(userID, "'", "\\'")),
			Limit: stripe.Int64(1),
		},
	}

	iter := customer.Search(params)
	for iter.Next() {
		return iter.Customer().ID, nil
	}
	if err := iter.Err(); err != nil {
		return "", fmt.Errorf("failed to search customers: %w", err)
	}
	return "", nil
}

// CreateCheckoutSession creates a Stripe Checkout session and returns the URL.
func (c *Client) CreateCheckoutSession(customerID, priceID, successURL, cancelURL string) (string, error) {
	params := &stripe.CheckoutSessionParams{
		Customer: stripe.String(customerID),
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceID),
				Quantity: stripe.Int64(1),
			},
		},
		SuccessURL: stripe.String(successURL),
		CancelURL:  stripe.String(cancelURL),
	}

	s, err := checkoutsession.New(params)
	if err != nil {
		return "", fmt.Errorf("failed to create checkout session: %w", err)
	}
	return s.URL, nil
}

// CreatePortalSession creates a Stripe Billing Portal session and returns the URL.
func (c *Client) CreatePortalSession(customerID, returnURL string) (string, error) {
	params := &stripe.BillingPortalSessionParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(returnURL),
	}

	s, err := portalsession.New(params)
	if err != nil {
		return "", fmt.Errorf("failed to create portal session: %w", err)
	}
	return s.URL, nil
}

// ParseWebhook verifies and parses a Stripe webhook payload.
//
// IgnoreAPIVersionMismatch keeps a version difference between the account and
// this pinned stripe-go release from rejecting every event (the SDK surfaces
// that rejection as a misleading signature error). The signature itself is
// still verified either way.
//
// Suppressing the check silently is what let an earlier drift go unnoticed:
// invoice.subscription had moved under invoice.parent and deserialized as nil
// on every event. So the mismatch is logged instead of ignored — once per
// distinct version, since it is a deploy-time condition, not a per-event one.
// Matching the account's version to stripe.APIVersion silences it.
func (c *Client) ParseWebhook(payload []byte, sig string) (stripe.Event, error) {
	event, err := webhook.ConstructEventWithOptions(payload, sig, c.cfg.WebhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return stripe.Event{}, fmt.Errorf("webhook signature verification failed: %w", err)
	}

	if event.APIVersion != "" && event.APIVersion != stripe.APIVersion {
		if _, seen := c.warnedVersions.LoadOrStore(event.APIVersion, struct{}{}); !seen {
			log.Printf("stripe webhook: account sends API version %s but stripe-go expects %s — "+
				"fields moved between these versions will deserialize as empty",
				event.APIVersion, stripe.APIVersion)
		}
	}
	return event, nil
}

// CancelSubscription cancels the Stripe subscription immediately.
func (c *Client) CancelSubscription(subID string) error {
	params := &stripe.SubscriptionCancelParams{}
	_, err := subscription.Cancel(subID, params)
	return err
}

// SetCancelAtPeriodEnd schedules (or unschedules) cancellation of a Stripe
// subscription at the end of the current billing period. The subscription stays
// active until then; Stripe emits customer.subscription.deleted when it lapses.
func (c *Client) SetCancelAtPeriodEnd(subID string, cancel bool) error {
	_, err := subscription.Update(subID, &stripe.SubscriptionParams{
		CancelAtPeriodEnd: stripe.Bool(cancel),
	})
	return err
}

// GetActiveSubscription returns the customer's current subscription, preferring
// an active/trialing/past_due one over a canceled one. Returns (nil, nil) when
// the customer has no subscriptions.
//
// REST responses are pinned to this stripe-go release's API version, so they
// deserialize predictably regardless of the account's default — making this
// the source of truth for display.
func (c *Client) GetActiveSubscription(customerID string) (*stripe.Subscription, error) {
	params := &stripe.SubscriptionListParams{
		Customer: stripe.String(customerID),
		Status:   stripe.String("all"),
	}
	params.Limit = stripe.Int64(20)

	iter := subscription.List(params)
	var fallback *stripe.Subscription
	for iter.Next() {
		s := iter.Subscription()
		switch s.Status {
		case stripe.SubscriptionStatusActive, stripe.SubscriptionStatusTrialing, stripe.SubscriptionStatusPastDue:
			return s, nil
		default:
			if fallback == nil {
				fallback = s
			}
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}
	return fallback, nil
}

// GetCustomer retrieves a customer by ID from Stripe.
func (c *Client) GetCustomer(customerID string) (*stripe.Customer, error) {
	return customer.Get(customerID, nil)
}

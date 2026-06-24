package stripeservice

import (
	"fmt"

	"github.com/stripe/stripe-go/v76"
	portalsession "github.com/stripe/stripe-go/v76/billingportal/session"
	checkoutsession "github.com/stripe/stripe-go/v76/checkout/session"
	"github.com/stripe/stripe-go/v76/customer"
	"github.com/stripe/stripe-go/v76/subscription"
	"github.com/stripe/stripe-go/v76/webhook"

	"upguardly-backend/internal/config"
)

type Client struct {
	cfg config.StripeConfig
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

// EnsureCustomer looks up or creates a Stripe customer for the org.
func (c *Client) EnsureCustomer(orgID, orgName string) (string, error) {
	// Search for existing customer by metadata
	iter := customer.List(&stripe.CustomerListParams{})
	for iter.Next() {
		cust := iter.Customer()
		if cust.Metadata["org_id"] == orgID {
			return cust.ID, nil
		}
	}
	if err := iter.Err(); err != nil {
		return "", fmt.Errorf("failed to search customers: %w", err)
	}

	// Create new customer
	params := &stripe.CustomerParams{
		Name: stripe.String(orgName),
	}
	params.AddMetadata("org_id", orgID)

	cust, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("failed to create Stripe customer: %w", err)
	}
	return cust.ID, nil
}

// EnsureCustomerForUser looks up or creates a Stripe customer keyed to a user,
// used for the checkout-first org-creation flow where no org exists yet. The
// resulting customer is tagged with user_id metadata; org_id is added later via
// SetCustomerOrgID once the org has been created by the webhook.
func (c *Client) EnsureCustomerForUser(userID, name string) (string, error) {
	iter := customer.List(&stripe.CustomerListParams{})
	for iter.Next() {
		cust := iter.Customer()
		// Only reuse a user-keyed customer that is not yet bound to an org, so we
		// never collide with an existing org's billing customer.
		if cust.Metadata["user_id"] == userID && cust.Metadata["org_id"] == "" {
			return cust.ID, nil
		}
	}
	if err := iter.Err(); err != nil {
		return "", fmt.Errorf("failed to search customers: %w", err)
	}

	params := &stripe.CustomerParams{Name: stripe.String(name)}
	params.AddMetadata("user_id", userID)

	cust, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("failed to create Stripe customer: %w", err)
	}
	return cust.ID, nil
}

// SetCustomerOrgID stamps the org_id metadata onto a customer once the org has
// been created, so the subscription.updated/deleted/payment_failed webhooks
// (which resolve the org via customer metadata) work for it going forward.
func (c *Client) SetCustomerOrgID(customerID, orgID string) error {
	params := &stripe.CustomerParams{}
	params.AddMetadata("org_id", orgID)
	if _, err := customer.Update(customerID, params); err != nil {
		return fmt.Errorf("failed to update customer metadata: %w", err)
	}
	return nil
}

// GetSubscription fetches a subscription from Stripe by ID. Used by the
// checkout-completed webhook to populate plan period dates and price at creation.
func (c *Client) GetSubscription(subID string) (*stripe.Subscription, error) {
	s, err := subscription.Get(subID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch subscription: %w", err)
	}
	return s, nil
}

// CreateOrgCheckoutSession creates a Checkout session for the checkout-first
// org-creation flow. The provided metadata (user_id, org_name, plan) is attached
// to both the session and the resulting subscription so the webhook can create
// the org after payment succeeds.
func (c *Client) CreateOrgCheckoutSession(customerID, priceID, successURL, cancelURL string, metadata map[string]string) (string, error) {
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
		SubscriptionData: &stripe.CheckoutSessionSubscriptionDataParams{
			Metadata: metadata,
		},
	}
	for k, v := range metadata {
		params.AddMetadata(k, v)
	}

	s, err := checkoutsession.New(params)
	if err != nil {
		return "", fmt.Errorf("failed to create checkout session: %w", err)
	}
	return s.URL, nil
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
func (c *Client) ParseWebhook(payload []byte, sig string) (stripe.Event, error) {
	event, err := webhook.ConstructEvent(payload, sig, c.cfg.WebhookSecret)
	if err != nil {
		return stripe.Event{}, fmt.Errorf("webhook signature verification failed: %w", err)
	}
	return event, nil
}

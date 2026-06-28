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

// EnsureCustomer looks up or creates the Stripe customer for a user (the
// billing subject), keyed on user_id metadata. The metadata is what the
// subscription webhooks resolve the subscription back to.
func (c *Client) EnsureCustomer(userID, name string) (string, error) {
	// Search for an existing customer by user_id metadata.
	iter := customer.List(&stripe.CustomerListParams{})
	for iter.Next() {
		cust := iter.Customer()
		if cust.Metadata["user_id"] == userID {
			return cust.ID, nil
		}
	}
	if err := iter.Err(); err != nil {
		return "", fmt.Errorf("failed to search customers: %w", err)
	}

	params := &stripe.CustomerParams{}
	if name != "" {
		params.Name = stripe.String(name)
	}
	params.AddMetadata("user_id", userID)

	cust, err := customer.New(params)
	if err != nil {
		return "", fmt.Errorf("failed to create Stripe customer: %w", err)
	}
	return cust.ID, nil
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

// CancelSubscription cancels the Stripe subscription.
func (c *Client) CancelSubscription(subID string) error {
	params := &stripe.SubscriptionCancelParams{}
	_, err := subscription.Cancel(subID, params)
	return err
}

// GetCustomer retrieves a customer by ID from Stripe.
func (c *Client) GetCustomer(customerID string) (*stripe.Customer, error) {
	return customer.Get(customerID, nil)
}

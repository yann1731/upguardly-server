package handlers

import "github.com/stripe/stripe-go/v76"

// StripeService is the subset of the Stripe client the handlers depend on.
// Defining it as an interface lets tests inject a fake implementation.
// A nil StripeService means billing is not configured.
type StripeService interface {
	PriceIDForPlan(plan string) (string, error)
	// EnsureCustomer looks up or creates the Stripe customer for a user (the
	// billing subject), keyed on user_id metadata.
	EnsureCustomer(userID, name string) (string, error)
	CreateCheckoutSession(customerID, priceID, successURL, cancelURL string) (string, error)
	CreatePortalSession(customerID, returnURL string) (string, error)
	ParseWebhook(payload []byte, sig string) (stripe.Event, error)
	CancelSubscription(subID string) error
	// SetCancelAtPeriodEnd schedules cancellation at the end of the current
	// billing period (the user keeps access until then).
	SetCancelAtPeriodEnd(subID string, cancel bool) error
	GetCustomer(customerID string) (*stripe.Customer, error)
	// FindCustomerByUser returns the Stripe customer ID for a user, or "" if
	// none exists. It never creates a customer.
	FindCustomerByUser(userID string) (string, error)
	// GetActiveSubscription returns the customer's current subscription, or nil.
	GetActiveSubscription(customerID string) (*stripe.Subscription, error)
}

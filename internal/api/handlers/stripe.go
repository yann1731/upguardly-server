package handlers

import "github.com/stripe/stripe-go/v76"

// StripeService is the subset of the Stripe client the handlers depend on.
// Defining it as an interface lets tests inject a fake implementation.
// A nil StripeService means billing is not configured.
type StripeService interface {
	PriceIDForPlan(plan string) (string, error)
	EnsureCustomer(orgID, orgName string) (string, error)
	CreateCheckoutSession(customerID, priceID, successURL, cancelURL string) (string, error)
	CreatePortalSession(customerID, returnURL string) (string, error)
	ParseWebhook(payload []byte, sig string) (stripe.Event, error)
}

package auth

import (
	"upguardly-backend/internal/config"
	"upguardly-backend/internal/mailer"

	"github.com/supertokens/supertokens-golang/ingredients/emaildelivery"
	"github.com/supertokens/supertokens-golang/recipe/emailpassword"
	"github.com/supertokens/supertokens-golang/recipe/emailverification"
	"github.com/supertokens/supertokens-golang/recipe/emailverification/evmodels"
	"github.com/supertokens/supertokens-golang/recipe/session"
	"github.com/supertokens/supertokens-golang/supertokens"
)

func Init(cfg *config.Config, m *mailer.Mailer) error {
	apiBasePath := "/auth"
	websiteBasePath := "/auth"

	return supertokens.Init(supertokens.TypeInput{
		Supertokens: &supertokens.ConnectionInfo{
			ConnectionURI: cfg.SuperTokens.ConnectionURI,
			APIKey:        cfg.SuperTokens.APIKey,
		},
		AppInfo: supertokens.AppInfo{
			AppName:         "UpGuardly",
			APIDomain:       cfg.SuperTokens.APIDomain,
			WebsiteDomain:   cfg.SuperTokens.WebsiteDomain,
			APIBasePath:     &apiBasePath,
			WebsiteBasePath: &websiteBasePath,
		},
		RecipeList: []supertokens.Recipe{
			emailpassword.Init(nil),
			emailverification.Init(evmodels.TypeInput{
				Mode: evmodels.ModeOptional,
				EmailDelivery: &emaildelivery.TypeInput{
					Override: func(original emaildelivery.EmailDeliveryInterface) emaildelivery.EmailDeliveryInterface {
						sendEmail := func(input emaildelivery.EmailType, _ supertokens.UserContext) error {
							ev := input.EmailVerification
							return m.SendVerificationEmail(ev.User.Email, ev.EmailVerifyLink)
						}
						original.SendEmail = &sendEmail
						return original
					},
				},
			}),
			session.Init(nil),
		},
	})
}

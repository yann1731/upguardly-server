package auth

import (
	"upguardly-backend/internal/config"

	"github.com/supertokens/supertokens-golang/recipe/emailpassword"
	"github.com/supertokens/supertokens-golang/recipe/session"
	"github.com/supertokens/supertokens-golang/supertokens"
)

func Init(cfg *config.Config) error {
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
			session.Init(nil),
		},
	})
}

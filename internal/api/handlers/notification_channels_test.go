package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"upguardly-backend/internal/models"
)

// stubEmail wires a fixed account email so tests never hit SuperTokens.
const stubbedEmail = "account@example.com"

func TestCreateNotificationChannel(t *testing.T) {
	t.Run("valid body returns 201", func(t *testing.T) {
		store := &mockStore{channelResult: aChannel()}
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return stubbedEmail, nil }
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"EMAIL"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("EMAIL target is pinned to the account email", func(t *testing.T) {
		store := &mockStore{channelResult: aChannel()}
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return stubbedEmail, nil }
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		// The caller-supplied target must be ignored.
		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"EMAIL","target":"attacker@example.com"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, stubbedEmail, store.lastChannelCreateTarget)
	})

	t.Run("non-EMAIL channel keeps the supplied target", func(t *testing.T) {
		store := &mockStore{channelResult: aChannel()}
		router, h := newTestRouter(store)
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"SMS","target":"+12125551234"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, "+12125551234", store.lastChannelCreateTarget)
	})

	t.Run("invalid target returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"SMS","target":"not-a-number"}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("gated channel returns 402 on FREE plan", func(t *testing.T) {
		store := &mockStore{} // no subscription → FREE
		router, h := newTestRouter(store)
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"SLACK","target":"https://hooks.slack.com/services/x"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	// EMAIL and SMS address the account holder, so they are one apiece on
	// every plan: a second add is a 409, not a 402 — upgrading wouldn't help.
	t.Run("second EMAIL integration returns 409", func(t *testing.T) {
		store := &mockStore{
			channelResult:  aChannel(),
			channelsResult: []models.NotificationChannel{{ID: "existing", Channel: models.AlertChannelEMAIL, Target: stubbedEmail}},
		}
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return stubbedEmail, nil }
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"EMAIL"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	t.Run("second SMS number returns 409", func(t *testing.T) {
		store := &mockStore{
			channelResult:  aChannel(),
			channelsResult: []models.NotificationChannel{{ID: "existing", Channel: models.AlertChannelSMS, Target: "+12125550000"}},
		}
		router, h := newTestRouter(store)
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"SMS","target":"+12125551234"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	// Webhook channels address a destination, not a person: several are fine.
	t.Run("second DISCORD webhook is allowed", func(t *testing.T) {
		store := &mockStore{
			channelResult:  aChannel(),
			channelsResult: []models.NotificationChannel{{ID: "existing", Channel: models.AlertChannelDISCORD, Target: "https://discord.com/api/webhooks/1/a"}},
		}
		router, h := newTestRouter(store)
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"DISCORD","target":"https://discord.com/api/webhooks/2/b"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
	})

	t.Run("duplicate DISCORD webhook returns 409", func(t *testing.T) {
		const hook = "https://discord.com/api/webhooks/1/a"
		store := &mockStore{
			channelResult:  aChannel(),
			channelsResult: []models.NotificationChannel{{ID: "existing", Channel: models.AlertChannelDISCORD, Target: hook}},
		}
		router, h := newTestRouter(store)
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"DISCORD","target":"`+hook+`"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
	})

	// FREE allows two Discord webhooks; the third is an upgrade prompt.
	t.Run("DISCORD cap reached on FREE returns 402", func(t *testing.T) {
		store := &mockStore{
			channelResult: aChannel(),
			channelsResult: []models.NotificationChannel{
				{ID: "one", Channel: models.AlertChannelDISCORD, Target: "https://discord.com/api/webhooks/1/a"},
				{ID: "two", Channel: models.AlertChannelDISCORD, Target: "https://discord.com/api/webhooks/2/b"},
			},
		}
		router, h := newTestRouter(store)
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"DISCORD","target":"https://discord.com/api/webhooks/3/c"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
	})

	// A cap counts only its own channel: an email and an SMS on file leave
	// Discord's two slots untouched.
	t.Run("other channels do not consume the DISCORD cap", func(t *testing.T) {
		store := &mockStore{
			channelResult: aChannel(),
			channelsResult: []models.NotificationChannel{
				{ID: "mail", Channel: models.AlertChannelEMAIL, Target: stubbedEmail},
				{ID: "sms", Channel: models.AlertChannelSMS, Target: "+12125550000"},
			},
		}
		router, h := newTestRouter(store)
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"DISCORD","target":"https://discord.com/api/webhooks/1/a"}`)

		assert.Equal(t, http.StatusCreated, w.Code)
	})
}

func TestUpdateNotificationChannel(t *testing.T) {
	t.Run("valid update returns 200", func(t *testing.T) {
		store := &mockStore{channelResult: aChannel()}
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return stubbedEmail, nil }
		router.PUT("/v1/notification-channels/:id", h.UpdateNotificationChannel)

		w := doRequest(router, "PUT", "/v1/notification-channels/chan-1", `{"enabled":false}`)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("EMAIL channel target stays pinned on update", func(t *testing.T) {
		store := &mockStore{channelResult: aChannel()} // existing channel is EMAIL
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return stubbedEmail, nil }
		router.PUT("/v1/notification-channels/:id", h.UpdateNotificationChannel)

		w := doRequest(router, "PUT", "/v1/notification-channels/chan-1", `{"target":"attacker@example.com"}`)

		assert.Equal(t, http.StatusOK, w.Code)
		if assert.NotNil(t, store.lastChannelUpdate) && assert.NotNil(t, store.lastChannelUpdate.Target) {
			assert.Equal(t, stubbedEmail, *store.lastChannelUpdate.Target)
		}
	})

	t.Run("channel not found returns 404", func(t *testing.T) {
		store := &mockStore{channelErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.PUT("/v1/notification-channels/:id", h.UpdateNotificationChannel)

		w := doRequest(router, "PUT", "/v1/notification-channels/missing", `{"enabled":false}`)

		assert.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("empty body returns 400", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.PUT("/v1/notification-channels/:id", h.UpdateNotificationChannel)

		w := doRequest(router, "PUT", "/v1/notification-channels/chan-1", `{}`)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	// The row being edited must not collide with itself.
	t.Run("re-saving a channel with its own target returns 200", func(t *testing.T) {
		const hook = "https://discord.com/api/webhooks/1/a"
		self := models.NotificationChannel{ID: "chan-1", Channel: models.AlertChannelDISCORD, Target: hook}
		store := &mockStore{channelResult: &self, channelsResult: []models.NotificationChannel{self}}
		router, h := newTestRouter(store)
		router.PUT("/v1/notification-channels/:id", h.UpdateNotificationChannel)

		w := doRequest(router, "PUT", "/v1/notification-channels/chan-1", `{"target":"`+hook+`"}`)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("retargeting onto another integration returns 409", func(t *testing.T) {
		const other = "https://discord.com/api/webhooks/2/b"
		self := models.NotificationChannel{ID: "chan-1", Channel: models.AlertChannelDISCORD, Target: "https://discord.com/api/webhooks/1/a"}
		store := &mockStore{
			channelResult: &self,
			channelsResult: []models.NotificationChannel{
				self,
				{ID: "chan-2", Channel: models.AlertChannelDISCORD, Target: other},
			},
		}
		router, h := newTestRouter(store)
		router.PUT("/v1/notification-channels/:id", h.UpdateNotificationChannel)

		w := doRequest(router, "PUT", "/v1/notification-channels/chan-1", `{"target":"`+other+`"}`)

		assert.Equal(t, http.StatusConflict, w.Code)
	})
}

func TestListNotificationChannels(t *testing.T) {
	t.Run("returns channels", func(t *testing.T) {
		store := &mockStore{channelsResult: []models.NotificationChannel{*aChannel()}}
		router, h := newTestRouter(store)
		router.GET("/v1/notification-channels", h.ListNotificationChannels)

		w := doRequest(router, "GET", "/v1/notification-channels", "")

		assert.Equal(t, http.StatusOK, w.Code)
	})
}

func TestDeleteNotificationChannel(t *testing.T) {
	t.Run("returns 204", func(t *testing.T) {
		store := &mockStore{}
		router, h := newTestRouter(store)
		router.DELETE("/v1/notification-channels/:id", h.DeleteNotificationChannel)

		w := doRequest(router, "DELETE", "/v1/notification-channels/chan-1", "")

		assert.Equal(t, http.StatusNoContent, w.Code)
	})

	t.Run("not found returns 404", func(t *testing.T) {
		store := &mockStore{deleteErr: models.ErrNotFound}
		router, h := newTestRouter(store)
		router.DELETE("/v1/notification-channels/:id", h.DeleteNotificationChannel)

		w := doRequest(router, "DELETE", "/v1/notification-channels/missing", "")

		assert.Equal(t, http.StatusNotFound, w.Code)
	})
}

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

	t.Run("channel quota reached returns 402", func(t *testing.T) {
		// FREE caps global channels at 3.
		store := &mockStore{channelCount: 3}
		router, h := newTestRouter(store)
		h.UserEmailLookup = func(string) (string, error) { return stubbedEmail, nil }
		router.POST("/v1/notification-channels", h.CreateNotificationChannel)

		w := doRequest(router, "POST", "/v1/notification-channels", `{"channel":"EMAIL"}`)

		assert.Equal(t, http.StatusPaymentRequired, w.Code)
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

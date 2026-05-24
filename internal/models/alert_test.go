package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateAlertRequest_SetDefaults(t *testing.T) {
	t.Run("sets enabled to true when nil", func(t *testing.T) {
		req := CreateAlertRequest{}
		req.SetDefaults()

		assert.NotNil(t, req.Enabled)
		assert.True(t, *req.Enabled)
	})

	t.Run("does not overwrite enabled=false", func(t *testing.T) {
		enabled := false
		req := CreateAlertRequest{Enabled: &enabled}
		req.SetDefaults()

		assert.NotNil(t, req.Enabled)
		assert.False(t, *req.Enabled)
	})
}

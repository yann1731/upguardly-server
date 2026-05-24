package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateMonitorRequest_SetDefaults(t *testing.T) {
	t.Run("sets all defaults when fields are zero", func(t *testing.T) {
		req := CreateMonitorRequest{}
		req.SetDefaults()

		assert.Equal(t, 60, req.Interval)
		assert.Equal(t, 30, req.Timeout)
		assert.NotNil(t, req.Enabled)
		assert.True(t, *req.Enabled)
	})

	t.Run("does not overwrite an already-set interval", func(t *testing.T) {
		req := CreateMonitorRequest{Interval: 120}
		req.SetDefaults()

		assert.Equal(t, 120, req.Interval)
		assert.Equal(t, 30, req.Timeout)
	})

	t.Run("does not overwrite an already-set timeout", func(t *testing.T) {
		req := CreateMonitorRequest{Timeout: 10}
		req.SetDefaults()

		assert.Equal(t, 60, req.Interval)
		assert.Equal(t, 10, req.Timeout)
	})

	t.Run("does not overwrite enabled=false", func(t *testing.T) {
		enabled := false
		req := CreateMonitorRequest{Enabled: &enabled}
		req.SetDefaults()

		assert.NotNil(t, req.Enabled)
		assert.False(t, *req.Enabled)
	})
}

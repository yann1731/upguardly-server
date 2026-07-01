package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCreateMonitorRequest_SetDefaults(t *testing.T) {
	t.Run("sets all defaults when fields are zero", func(t *testing.T) {
		req := CreateMonitorRequest{}
		req.SetDefaults()

		// Interval stays 0 ("not provided"): its default is the plan's
		// minimum, applied by the handler after resolving the plan.
		assert.Equal(t, 0, req.Interval)
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

		assert.Equal(t, 0, req.Interval)
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

func TestCreateMonitorRequest_ValidateInterval(t *testing.T) {
	valid := func() CreateMonitorRequest {
		return CreateMonitorRequest{Name: "m", Target: "https://example.com", Timeout: 30}
	}

	t.Run("interval 0 (not provided) passes bounds and timeout checks", func(t *testing.T) {
		req := valid() // Interval 0, Timeout 30
		assert.NoError(t, req.Validate())
	})

	t.Run("explicit interval below the global minimum is rejected", func(t *testing.T) {
		req := valid()
		req.Interval = MonitorIntervalMin - 1
		assert.Error(t, req.Validate())
	})

	t.Run("explicit timeout >= explicit interval is rejected", func(t *testing.T) {
		req := valid()
		req.Interval = 60
		req.Timeout = 60
		assert.Error(t, req.Validate())
	})
}

package bun

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"upguardly-backend/internal/models"
)

func (s *BunStore) FetchActiveMonitors(ctx context.Context, region string) ([]models.Monitor, error) {
	var monitors []Monitor
	err := s.client.DB.NewSelect().
		Model(&monitors).
		Where("enabled = true").
		Where("? = ANY(regions)", region).
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.Monitor, len(monitors))
	for i, m := range monitors {
		out[i] = m.toModel()
	}
	return out, nil
}

func (s *BunStore) FetchOwnedMonitors(ctx context.Context, region string, ownedPartitions []int, partitionSQLExpr string) ([]models.Monitor, error) {
	if len(ownedPartitions) == 0 {
		return nil, nil
	}

	ids := make([]string, len(ownedPartitions))
	for i, p := range ownedPartitions {
		ids[i] = strconv.Itoa(p)
	}

	query := fmt.Sprintf(
		`SELECT id, user_id, org_id, name, type, target,
		        "interval", timeout, enabled, regions, created_at, updated_at
		 FROM monitors
		 WHERE enabled = true AND ? = ANY(regions) AND %s IN (%s)`,
		partitionSQLExpr,
		strings.Join(ids, ","),
	)

	var monitors []Monitor
	err := s.client.DB.NewRaw(query, region).Scan(ctx, &monitors)
	if err != nil {
		return nil, err
	}

	out := make([]models.Monitor, len(monitors))
	for i := range monitors {
		out[i] = monitors[i].toModel()
	}
	return out, nil
}

func (s *BunStore) RecordRegionCheck(ctx context.Context, monitorID, region string, result *models.CheckResult) (string, error) {
	var statusCode interface{}
	if result.StatusCode != nil {
		statusCode = *result.StatusCode
	}

	type regionCheckRow struct {
		Transition string  `json:"transition"`
		IncidentID *string `json:"incidentId"`
	}

	var row regionCheckRow
	err := s.client.DB.NewRaw(
		`SELECT transition, incident_id AS "incidentId"
		   FROM maintenance.record_region_check(?::text, ?::text, ?::"Status", ?::int4, ?::int4, ?::text)`,
		monitorID, region, string(result.Status), result.Latency, statusCode, result.Message,
	).Scan(ctx, &row)
	if err != nil {
		return "none", err
	}
	return row.Transition, nil
}

func (s *BunStore) PersistMonitorResults(ctx context.Context, region string, results []models.PendingResult) error {
	if len(results) == 0 {
		return nil
	}

	dbResults := make([]MonitorResult, len(results))
	for i, r := range results {
		var statusCode *int
		if r.Result.StatusCode != nil {
			c := *r.Result.StatusCode
			statusCode = &c
		}
		var msg *string
		if r.Result.Message != "" {
			m := r.Result.Message
			msg = &m
		}
		dbResults[i] = MonitorResult{
			ID:         uuid.NewString(),
			MonitorID:  r.MonitorID,
			Status:     string(r.Result.Status),
			Latency:    r.Result.Latency,
			StatusCode: statusCode,
			Message:    msg,
			Region:     region,
			CheckedAt:  time.Now(),
		}
	}

	var sb strings.Builder
	sb.WriteString(`INSERT INTO monitor_results (id, monitor_id, status, latency, status_code, message, region) ` +
		`SELECT v.id, v.monitor_id, v.status::"Status", v.latency, v.status_code, v.message, v.region FROM (VALUES `)
	params := make([]interface{}, 0, len(dbResults)*7)
	for i, p := range dbResults {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(`(?::text, ?::text, ?::text, ?::int4, ?::int4, ?::text, ?::text)`)
		params = append(params, p.ID, p.MonitorID, p.Status, p.Latency, p.StatusCode, p.Message, p.Region)
	}
	sb.WriteString(`) AS v(id, monitor_id, status, latency, status_code, message, region) ` +
		`JOIN monitors ON monitors.id = v.monitor_id`)

	_, err := s.client.DB.NewRaw(sb.String(), params...).Exec(ctx)
	return err
}

func (s *BunStore) ClaimOutboxAlerts(ctx context.Context, limit int) ([]models.AlertOutboxRow, error) {
	claimQuery := fmt.Sprintf(`
	UPDATE alert_outbox
	SET attempts = attempts + 1,
	    next_attempt_at = now() + least(interval '30 seconds' * (2 ^ attempts), interval '30 minutes')
	WHERE id IN (
	    SELECT id FROM alert_outbox
	    WHERE next_attempt_at <= now()
	    ORDER BY next_attempt_at
	    LIMIT %d
	    FOR UPDATE SKIP LOCKED
	)
	RETURNING id, alert_id, notification_channel_id, monitor_id, channel, target,
	          status, message, status_code, latency, monitor_name, monitor_type,
	          monitor_target, attempts`, limit)

	var rows []AlertOutbox
	err := s.client.DB.NewRaw(claimQuery).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}

	out := make([]models.AlertOutboxRow, len(rows))
	for i, r := range rows {
		out[i] = models.AlertOutboxRow{
			ID:                    r.ID,
			AlertID:               r.AlertID,
			NotificationChannelID: r.NotificationChannelID,
			MonitorID:             r.MonitorID,
			Channel:               models.AlertChannel(r.Channel),
			Target:                r.Target,
			Status:                models.Status(r.Status),
			Message:               r.Message,
			StatusCode:            r.StatusCode,
			Latency:               r.Latency,
			MonitorName:           r.MonitorName,
			MonitorType:           models.MonitorType(r.MonitorType),
			MonitorTarget:         r.MonitorTarget,
			Attempts:              r.Attempts,
		}
	}
	return out, nil
}

func (s *BunStore) FinalizeOutboxAlert(ctx context.Context, id string, status models.Status, message string, alertID, notificationChannelID *string) error {
	tx, err := s.client.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if alertID != nil || notificationChannelID != nil {
		history := AlertHistory{
			ID:                    uuid.NewString(),
			AlertID:               alertID,
			NotificationChannelID: notificationChannelID,
			Status:                string(status),
			Message:               message,
			SentAt:                time.Now(),
		}
		if _, err := tx.NewInsert().Model(&history).Exec(ctx); err != nil {
			return err
		}
	}

	if _, err := tx.NewDelete().
		Table("alert_outbox").
		Where("id = ?", id).
		Exec(ctx); err != nil {
		return err
	}

	return tx.Commit()
}

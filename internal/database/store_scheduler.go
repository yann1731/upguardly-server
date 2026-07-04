package database

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/google/uuid"

	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
)

func (s *PrismaStore) FetchActiveMonitors(ctx context.Context, region string) ([]models.Monitor, error) {
	monitors, err := s.client.Prisma.Monitor.FindMany(
		db.Monitor.Enabled.Equals(true),
		db.Monitor.Regions.Has(region),
	).Exec(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.Monitor, len(monitors))
	for i := range monitors {
		out[i] = *monitorToModel(&monitors[i])
	}
	return out, nil
}

func (s *PrismaStore) FetchOwnedMonitors(ctx context.Context, region string, ownedPartitions []int, partitionSQLExpr string) ([]models.Monitor, error) {
	if len(ownedPartitions) == 0 {
		return nil, nil
	}

	ids := make([]string, len(ownedPartitions))
	for i, p := range ownedPartitions {
		ids[i] = strconv.Itoa(p)
	}

	query := fmt.Sprintf(
		`SELECT id, user_id AS "userId", org_id AS "orgId", name, type, target,
		        "interval", timeout, enabled, regions, created_at AS "createdAt", updated_at AS "updatedAt"
		 FROM monitors
		 WHERE enabled = true AND $1 = ANY(regions) AND %s IN (%s)`,
		partitionSQLExpr,
		strings.Join(ids, ","),
	)

	var raws []db.RawMonitorModel
	if err := s.client.Prisma.Prisma.QueryRaw(query, region).Exec(ctx, &raws); err != nil {
		return nil, err
	}

	monitors := make([]models.Monitor, len(raws))
	for i := range raws {
		r := &raws[i]
		regions := make([]string, len(r.Regions))
		for j, reg := range r.Regions {
			regions[j] = string(reg)
		}
		var orgID *string
		if r.OrgID != nil {
			o := string(*r.OrgID)
			orgID = &o
		}
		monitors[i] = models.Monitor{
			ID:        string(r.ID),
			OrgID:     orgID,
			Name:      string(r.Name),
			Type:      models.MonitorType(r.Type),
			Target:    string(r.Target),
			Interval:  int(r.Interval),
			Timeout:   int(r.Timeout),
			Enabled:   bool(r.Enabled),
			Regions:   regions,
			CreatedAt: r.CreatedAt.Time,
			UpdatedAt: r.UpdatedAt.Time,
		}
	}
	return monitors, nil
}

func (s *PrismaStore) RecordRegionCheck(ctx context.Context, monitorID, region string, result *models.CheckResult) (string, error) {
	var statusCode interface{}
	if result.StatusCode != nil {
		statusCode = *result.StatusCode
	}

	type regionCheckRow struct {
		Transition db.RawString  `json:"transition"`
		IncidentID *db.RawString `json:"incidentId"`
	}

	var rows []regionCheckRow
	err := s.client.Prisma.Prisma.QueryRaw(
		`SELECT transition, incident_id AS "incidentId"
		   FROM maintenance.record_region_check($1::text, $2::text, $3::"Status", $4::int4, $5::int4, $6::text)`,
		monitorID, region, string(result.Status), result.Latency, statusCode, result.Message,
	).Exec(ctx, &rows)
	if err != nil {
		return "none", err
	}
	if len(rows) == 0 {
		return "none", nil
	}
	return string(rows[0].Transition), nil
}

func (s *PrismaStore) PersistMonitorResults(ctx context.Context, region string, results []models.PendingResult) error {
	if len(results) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(`INSERT INTO monitor_results (id, monitor_id, status, latency, status_code, message, region) ` +
		`SELECT v.id, v.monitor_id, v.status::"Status", v.latency, v.status_code, v.message, v.region FROM (VALUES `)
	params := make([]interface{}, 0, len(results)*7)
	for i, p := range results {
		if i > 0 {
			sb.WriteString(", ")
		}
		n := len(params)
		fmt.Fprintf(&sb, `($%d::text, $%d::text, $%d::text, $%d::int4, $%d::int4, $%d::text, $%d::text)`,
			n+1, n+2, n+3, n+4, n+5, n+6, n+7)
		var statusCode interface{}
		if p.Result.StatusCode != nil {
			statusCode = *p.Result.StatusCode
		}
		params = append(params,
			uuid.NewString(), p.MonitorID, string(p.Result.Status),
			p.Result.Latency, statusCode, p.Result.Message, region,
		)
	}
	sb.WriteString(`) AS v(id, monitor_id, status, latency, status_code, message, region) ` +
		`JOIN monitors ON monitors.id = v.monitor_id`)

	_, err := s.client.Prisma.Prisma.ExecuteRaw(sb.String(), params...).Exec(ctx)
	if err != nil {
		log.Printf("Batch result insert failed (%d rows), retrying individually: %v", len(results), err)
		for _, p := range results {
			if err := s.insertResultOne(ctx, region, p); err != nil {
				log.Printf("Failed to insert single result for monitor %s: %v", p.MonitorID, err)
			}
		}
	}
	return nil
}

func (s *PrismaStore) insertResultOne(ctx context.Context, region string, p models.PendingResult) error {
	optionalParams := []db.MonitorResultSetParam{
		db.MonitorResult.Message.Set(p.Result.Message),
		db.MonitorResult.Region.Set(region),
	}
	if p.Result.StatusCode != nil {
		optionalParams = append(optionalParams, db.MonitorResult.StatusCode.Set(*p.Result.StatusCode))
	}

	_, err := s.client.Prisma.MonitorResult.CreateOne(
		db.MonitorResult.Monitor.Link(db.Monitor.ID.Equals(p.MonitorID)),
		db.MonitorResult.Status.Set(db.Status(p.Result.Status)),
		db.MonitorResult.Latency.Set(p.Result.Latency),
		optionalParams...,
	).Exec(ctx)
	return err
}

func (s *PrismaStore) ClaimOutboxAlerts(ctx context.Context, limit int) ([]models.AlertOutboxRow, error) {
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
	RETURNING id, alert_id AS "alertId", notification_channel_id AS "notificationChannelId",
	          monitor_id AS "monitorId", channel, target,
	          status, message, status_code AS "statusCode", latency,
	          monitor_name AS "monitorName", monitor_type AS "monitorType",
	          monitor_target AS "monitorTarget", attempts`, limit)

	type outboxRow struct {
		ID                    db.RawString  `json:"id"`
		AlertID               *db.RawString `json:"alertId"`
		NotificationChannelID *db.RawString `json:"notificationChannelId"`
		MonitorID             db.RawString  `json:"monitorId"`
		Channel               db.RawString  `json:"channel"`
		Target                db.RawString  `json:"target"`
		Status                db.RawString  `json:"status"`
		Message               db.RawString  `json:"message"`
		StatusCode            *db.RawInt    `json:"statusCode"`
		Latency               db.RawInt     `json:"latency"`
		MonitorName           db.RawString  `json:"monitorName"`
		MonitorType           db.RawString  `json:"monitorType"`
		MonitorTarget         db.RawString  `json:"monitorTarget"`
		Attempts              db.RawInt     `json:"attempts"`
	}

	var rows []outboxRow
	if err := s.client.Prisma.Prisma.QueryRaw(claimQuery).Exec(ctx, &rows); err != nil {
		return nil, err
	}

	out := make([]models.AlertOutboxRow, len(rows))
	for i := range rows {
		r := &rows[i]
		var alertID *string
		if r.AlertID != nil {
			a := string(*r.AlertID)
			alertID = &a
		}
		var notificationChannelID *string
		if r.NotificationChannelID != nil {
			n := string(*r.NotificationChannelID)
			notificationChannelID = &n
		}
		var statusCode *int
		if r.StatusCode != nil {
			c := int(*r.StatusCode)
			statusCode = &c
		}

		out[i] = models.AlertOutboxRow{
			ID:                    string(r.ID),
			AlertID:               alertID,
			NotificationChannelID: notificationChannelID,
			MonitorID:             string(r.MonitorID),
			Channel:               models.AlertChannel(r.Channel),
			Target:                string(r.Target),
			Status:                models.Status(r.Status),
			Message:               string(r.Message),
			StatusCode:            statusCode,
			Latency:               int(r.Latency),
			MonitorName:           string(r.MonitorName),
			MonitorType:           models.MonitorType(r.MonitorType),
			MonitorTarget:         string(r.MonitorTarget),
			Attempts:              int(r.Attempts),
		}
	}
	return out, nil
}

func (s *PrismaStore) FinalizeOutboxAlert(ctx context.Context, id string, status models.Status, message string, alertID, notificationChannelID *string) error {
	var source db.AlertHistorySetParam
	switch {
	case alertID != nil:
		source = db.AlertHistory.Alert.Link(db.Alert.ID.Equals(*alertID))
	case notificationChannelID != nil:
		source = db.AlertHistory.NotificationChannel.Link(db.NotificationChannel.ID.Equals(*notificationChannelID))
	}

	if source != nil {
		if _, err := s.client.Prisma.AlertHistory.CreateOne(
			db.AlertHistory.Status.Set(db.Status(status)),
			db.AlertHistory.Message.Set(message),
			source,
		).Exec(ctx); err != nil {
			log.Printf("Failed to save alert history for outbox row %s: %v", id, err)
		}
	} else {
		log.Printf("Outbox row %s has no source; skipping history", id)
	}

	if _, err := s.client.Prisma.AlertOutbox.FindUnique(
		db.AlertOutbox.ID.Equals(id),
	).Delete().Exec(ctx); err != nil {
		return err
	}
	return nil
}

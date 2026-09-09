package bun

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"upguardly-backend/internal/models"
	moncheck "upguardly-backend/internal/monitor"
)

func (s *BunStore) FetchActiveMonitors(ctx context.Context, region string) ([]models.Monitor, error) {
	var monitors []Monitor
	err := s.client.DB.NewSelect().
		Model(&monitors).
		ColumnExpr("m.*").
		ColumnExpr(ownerPlanExpr("m")+" AS owner_plan").
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
		        "interval", timeout, degraded_threshold_ms, enabled, regions, created_at, updated_at,
		        %s AS owner_plan
		 FROM monitors
		 WHERE enabled = true AND ? = ANY(regions) AND %s IN (%s)`,
		ownerPlanExpr("monitors"),
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

func (s *BunStore) RecordRegionCheck(ctx context.Context, monitorID, region string, result *models.CheckResult, source models.CheckSource) (string, error) {
	var statusCode interface{}
	if result.StatusCode != nil {
		statusCode = *result.StatusCode
	}

	type regionCheckRow struct {
		Transition string  `bun:"transition"`
		IncidentID *string `bun:"incident_id"`
	}

	var row regionCheckRow
	err := s.client.DB.NewRaw(
		`SELECT transition, incident_id
		   FROM maintenance.record_region_check(?::text, ?::text, ?::"Status", ?::int4, ?::int4, ?::text, ?::int4, ?::text)`,
		monitorID, region, string(result.Status), result.Latency, statusCode, result.Message,
		models.RegionStaleMultiplier, string(source),
	).Scan(ctx, &row)
	if err != nil {
		return "none", err
	}
	return row.Transition, nil
}

// UpsertRegionHeartbeat marks this region's scheduler pool alive. Called on a
// short timer; a region counts toward alert quorum only while its heartbeat is
// recent (see maintenance.evaluate_monitor_quorum).
func (s *BunStore) UpsertRegionHeartbeat(ctx context.Context, region string) error {
	_, err := s.client.DB.NewRaw(
		`INSERT INTO scheduler_region_heartbeats (region, last_seen_at)
		 VALUES (?::text, now())
		 ON CONFLICT (region) DO UPDATE SET last_seen_at = now()`,
		region,
	).Exec(ctx)
	return err
}

// ClaimVerificationRequests atomically claims up to limit unexpired, unclaimed
// confirmation checks for this region, joining the monitor so the verifier can
// run the check without a second query. FOR UPDATE SKIP LOCKED lets multiple
// instances in a region drain the queue without stepping on each other.
func (s *BunStore) ClaimVerificationRequests(ctx context.Context, region string, limit int) ([]models.VerificationRequest, error) {
	claimQuery := fmt.Sprintf(`
	UPDATE region_verification_requests r
	SET claimed_at = now(), claimed_by = ?
	FROM monitors m
	WHERE m.id = r.monitor_id
	  AND r.id IN (
	      SELECT id FROM region_verification_requests
	      WHERE region = ? AND claimed_at IS NULL AND expires_at > now()
	      ORDER BY requested_at
	      LIMIT %d
	      FOR UPDATE SKIP LOCKED
	  )
	  AND m.enabled = true
	RETURNING r.id, r.monitor_id, r.region, m.type, m.target, m.timeout`, limit)

	type claimRow struct {
		ID        string `bun:"id"`
		MonitorID string `bun:"monitor_id"`
		Region    string `bun:"region"`
		Type      string `bun:"type"`
		Target    string `bun:"target"`
		Timeout   int    `bun:"timeout"`
	}

	var rows []claimRow
	if err := s.client.DB.NewRaw(claimQuery, region, region).Scan(ctx, &rows); err != nil {
		return nil, err
	}

	out := make([]models.VerificationRequest, len(rows))
	for i, r := range rows {
		out[i] = models.VerificationRequest{
			ID:        r.ID,
			MonitorID: r.MonitorID,
			Region:    r.Region,
			Type:      models.MonitorType(r.Type),
			Target:    r.Target,
			Timeout:   r.Timeout,
		}
	}
	return out, nil
}

// CompleteVerificationRequest removes a confirmation request once its check has
// been recorded.
func (s *BunStore) CompleteVerificationRequest(ctx context.Context, id string) error {
	_, err := s.client.DB.NewRaw(
		`DELETE FROM region_verification_requests WHERE id = ?::text`, id,
	).Exec(ctx)
	return err
}

// ExpireVerificationRequests deletes past-expiry requests and returns the
// distinct monitor ids affected so quorum can be re-run without them.
func (s *BunStore) ExpireVerificationRequests(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.client.DB.NewRaw(
		`DELETE FROM region_verification_requests
		  WHERE id IN (
		      SELECT id FROM region_verification_requests
		       WHERE expires_at <= now()
		       FOR UPDATE SKIP LOCKED
		  )
		 RETURNING monitor_id`,
	).Scan(ctx, &ids)
	if err != nil {
		return nil, err
	}
	// Distinct monitor ids (a monitor may have multiple expired requests).
	seen := make(map[string]bool, len(ids))
	out := ids[:0]
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// EvaluateMonitorQuorum re-runs the quorum decision for a monitor with no
// triggering check (used by the expiry sweep).
func (s *BunStore) EvaluateMonitorQuorum(ctx context.Context, monitorID string) (string, error) {
	type quorumRow struct {
		Transition string  `bun:"transition"`
		IncidentID *string `bun:"incident_id"`
	}
	var row quorumRow
	err := s.client.DB.NewRaw(
		`SELECT transition, incident_id
		   FROM maintenance.evaluate_monitor_quorum(?::text, NULL::int4, 0::int4, NULL::text)`,
		monitorID,
	).Scan(ctx, &row)
	if err != nil {
		return "none", err
	}
	return row.Transition, nil
}

// ClaimDueExpiryChecks hands the expiry sweep its next batch of due
// cert/domain sub-checks. Placeholder status rows are inserted lazily for
// newly enabled monitors; claiming advances checked_at (with SKIP LOCKED on
// the select), so concurrent instances never double-check and a crashed sweep
// simply retries the row in 24h.
func (s *BunStore) ClaimDueExpiryChecks(ctx context.Context, limit int) ([]models.ExpiryCheckClaim, error) {
	// Seed missing status rows as immediately due (epoch checked_at).
	if _, err := s.client.DB.NewRaw(`
		INSERT INTO monitor_expiry_status (monitor_id, kind, checked_at)
		SELECT m.id, k.kind, to_timestamp(0)
		  FROM monitors m
		 CROSS JOIN (VALUES ('CERT'), ('DOMAIN')) AS k(kind)
		 WHERE m.type = 'HTTP'
		   AND ((k.kind = 'CERT' AND m.cert_check_enabled)
		     OR (k.kind = 'DOMAIN' AND m.domain_check_enabled))
		 ON CONFLICT DO NOTHING`).Exec(ctx); err != nil {
		return nil, err
	}

	type claimRow struct {
		MonitorID     string `bun:"monitor_id"`
		Kind          string `bun:"kind"`
		Target        string `bun:"target"`
		ThresholdDays int    `bun:"threshold_days"`
	}
	var rows []claimRow
	err := s.client.DB.NewRaw(`
		WITH due AS (
			SELECT s.monitor_id, s.kind
			  FROM monitor_expiry_status s
			  JOIN monitors m ON m.id = s.monitor_id
			 WHERE s.checked_at < now() - interval '24 hours'
			   AND m.type = 'HTTP'
			   AND ((s.kind = 'CERT' AND m.cert_check_enabled)
			     OR (s.kind = 'DOMAIN' AND m.domain_check_enabled))
			 ORDER BY s.checked_at
			 LIMIT ?
			   FOR UPDATE OF s SKIP LOCKED
		)
		UPDATE monitor_expiry_status s
		   SET checked_at = now()
		  FROM due
		  JOIN monitors m ON m.id = due.monitor_id
		 WHERE s.monitor_id = due.monitor_id AND s.kind = due.kind
		RETURNING s.monitor_id, s.kind, m.target,
		          CASE WHEN s.kind = 'CERT' THEN m.cert_expiry_threshold_days
		               ELSE m.domain_expiry_threshold_days END AS threshold_days`,
		limit,
	).Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}

	out := make([]models.ExpiryCheckClaim, len(rows))
	for i, r := range rows {
		out[i] = models.ExpiryCheckClaim{
			MonitorID:     r.MonitorID,
			Kind:          models.ExpiryKind(r.Kind),
			Target:        r.Target,
			ThresholdDays: r.ThresholdDays,
		}
	}
	return out, nil
}

// RecordExpiryCheck stores one expiry sub-check outcome and, when the expiry
// crosses a new alert bucket (threshold/3d/1d/expired — see
// models.ExpiryAlertBucket), fans an alert out through
// maintenance.enqueue_monitor_alerts (respecting maintenance windows). A
// checkErr records last_error and touches nothing else — stale expiry data
// keeps its bucket state so recovery doesn't re-alert.
func (s *BunStore) RecordExpiryCheck(ctx context.Context, claim models.ExpiryCheckClaim, expiresAt *time.Time, checkErr string) error {
	if checkErr != "" {
		_, err := s.client.DB.NewRaw(`
			UPDATE monitor_expiry_status SET last_error = ?
			 WHERE monitor_id = ? AND kind = ?`,
			checkErr, claim.MonitorID, string(claim.Kind),
		).Exec(ctx)
		return err
	}
	if expiresAt == nil {
		return fmt.Errorf("expiry check for %s/%s returned neither a date nor an error", claim.MonitorID, claim.Kind)
	}

	return s.client.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var st MonitorExpiryStatus
		if err := tx.NewSelect().Model(&st).
			Where("monitor_id = ?", claim.MonitorID).
			Where("kind = ?", string(claim.Kind)).
			For("UPDATE").
			Scan(ctx); err != nil {
			return err
		}

		daysLeft := models.ExpiryDaysLeft(*expiresAt, time.Now())
		bucket := models.ExpiryAlertBucket(daysLeft, claim.ThresholdDays)

		// A bucket fires once per expires_at value: re-arm on renewal
		// (alerted_expires_at mismatch), then only on deeper buckets.
		shouldAlert := bucket != nil &&
			(st.AlertedExpiresAt == nil || !st.AlertedExpiresAt.Equal(*expiresAt) ||
				st.LastAlertBucket == nil || *bucket < *st.LastAlertBucket)

		upd := tx.NewUpdate().Model((*MonitorExpiryStatus)(nil)).
			Set("expires_at = ?", *expiresAt).
			Set("last_error = NULL").
			Where("monitor_id = ?", claim.MonitorID).
			Where("kind = ?", string(claim.Kind))
		if shouldAlert {
			upd = upd.Set("last_alert_bucket = ?", *bucket).
				Set("alerted_expires_at = ?", *expiresAt)
		}
		if _, err := upd.Exec(ctx); err != nil {
			return err
		}

		if !shouldAlert {
			return nil
		}

		var inMaintenance bool
		if err := tx.NewRaw(`SELECT maintenance.in_maintenance(?)`, claim.MonitorID).Scan(ctx, &inMaintenance); err != nil {
			return err
		}
		if inMaintenance {
			return nil
		}

		status, message := expiryAlertContent(claim, *expiresAt, daysLeft)
		var enqueued int
		return tx.NewRaw(
			`SELECT maintenance.enqueue_monitor_alerts(?, ?::"Status", ?, NULL::int4, NULL::int4)`,
			claim.MonitorID, status, message,
		).Scan(ctx, &enqueued)
	})
}

// expiryAlertContent renders the outbox status/message for an expiry alert.
// The "Status" enum has no expiry-specific value: DEGRADED = approaching,
// DOWN = expired; the message carries the real content.
func expiryAlertContent(claim models.ExpiryCheckClaim, expiresAt time.Time, daysLeft int) (string, string) {
	subject := "TLS certificate for " + claim.Target
	if claim.Kind == models.ExpiryKindDomain {
		if domain, err := moncheck.RegistrableDomain(claim.Target); err == nil {
			subject = "Domain " + domain
		} else {
			subject = "Domain for " + claim.Target
		}
	}
	date := expiresAt.UTC().Format("2006-01-02")
	switch {
	case daysLeft < 0:
		return "DOWN", fmt.Sprintf("%s has expired (%s)", subject, date)
	case daysLeft == 0:
		return "DEGRADED", fmt.Sprintf("%s expires today (%s)", subject, date)
	case daysLeft == 1:
		return "DEGRADED", fmt.Sprintf("%s expires in 1 day (%s)", subject, date)
	default:
		return "DEGRADED", fmt.Sprintf("%s expires in %d days (%s)", subject, daysLeft, date)
	}
}

// EnqueueRepeatAlerts runs one pass of the repeat-alert sweep. All state
// logic lives in maintenance.enqueue_repeat_alerts (migration 20260721120000):
// due open incidents are locked with SKIP LOCKED, re-alerted through
// maintenance.enqueue_monitor_alerts, and their repeats_sent/last_alert_at
// advanced — concurrent scheduler instances can't double-send.
func (s *BunStore) EnqueueRepeatAlerts(ctx context.Context) (int, error) {
	var swept int
	err := s.client.DB.NewRaw(`SELECT maintenance.enqueue_repeat_alerts()`).Scan(ctx, &swept)
	if err != nil {
		return 0, err
	}
	return swept, nil
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

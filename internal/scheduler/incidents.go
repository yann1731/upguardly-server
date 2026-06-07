package scheduler

import (
	"context"
	"log"
	"time"

	"upguardly-backend/internal/database"
	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
)

// recordIncident maintains the incidents table from a check result. It is
// idempotent and safe to call on every check (a monitor maps to exactly one
// scheduler goroutine/owner, so there are no concurrent writers for the same
// monitor):
//
//   - unhealthy result + no open incident  -> open a new incident
//   - unhealthy result + open incident      -> update its latest message/status
//   - healthy result   + open incident      -> resolve it
//   - healthy result   + no open incident   -> no-op
//
// Because it keys off the persisted open-incident row rather than in-memory
// transition state, it correctly opens an incident when a monitor is first
// observed unhealthy or after a process restart.
func recordIncident(ctx context.Context, dbc *database.Client, monitorID string, r *models.CheckResult) {
	open, err := dbc.Prisma.Incident.FindFirst(
		db.Incident.MonitorID.Equals(monitorID),
		db.Incident.ResolvedAt.IsNull(),
	).OrderBy(
		db.Incident.StartedAt.Order(db.DESC),
	).Exec(ctx)
	if err != nil && !db.IsErrNotFound(err) {
		log.Printf("Failed to look up open incident for %s: %v", monitorID, err)
		return
	}
	hasOpen := err == nil && open != nil

	healthy := r.Status == models.StatusUP

	switch {
	case !healthy && !hasOpen:
		params := []db.IncidentSetParam{
			db.Incident.Message.Set(r.Message),
		}
		if r.StatusCode != nil {
			params = append(params, db.Incident.StatusCode.Set(*r.StatusCode))
		}
		if _, err := dbc.Prisma.Incident.CreateOne(
			db.Incident.Monitor.Link(db.Monitor.ID.Equals(monitorID)),
			db.Incident.Status.Set(db.Status(r.Status)),
			params...,
		).Exec(ctx); err != nil {
			log.Printf("Failed to open incident for %s: %v", monitorID, err)
		}

	case !healthy && hasOpen:
		params := []db.IncidentSetParam{
			db.Incident.Message.Set(r.Message),
			db.Incident.Status.Set(worstStatus(models.Status(open.Status), r.Status)),
		}
		if r.StatusCode != nil {
			params = append(params, db.Incident.StatusCode.Set(*r.StatusCode))
		}
		if _, err := dbc.Prisma.Incident.FindUnique(
			db.Incident.ID.Equals(open.ID),
		).Update(params...).Exec(ctx); err != nil {
			log.Printf("Failed to update incident %s: %v", open.ID, err)
		}

	case healthy && hasOpen:
		if _, err := dbc.Prisma.Incident.FindUnique(
			db.Incident.ID.Equals(open.ID),
		).Update(
			db.Incident.ResolvedAt.Set(time.Now()),
		).Exec(ctx); err != nil {
			log.Printf("Failed to resolve incident %s: %v", open.ID, err)
		}
	}
}

// worstStatus keeps the more severe of two unhealthy statuses (DOWN > DEGRADED)
// so an incident that ever went fully DOWN is recorded as DOWN.
func worstStatus(a, b models.Status) db.Status {
	if a == models.StatusDOWN || b == models.StatusDOWN {
		return db.Status(models.StatusDOWN)
	}
	return db.Status(b)
}

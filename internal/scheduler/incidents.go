package scheduler

import (
	"context"
	"log"
	"time"

	"upguardly-backend/internal/database"
	db "upguardly-backend/internal/database/prisma"
	"upguardly-backend/internal/models"
)

// incidentTransition describes what incidentTracker.record did with a check
// result, and is the single source of truth for when alerts fire. Because it
// is derived from the persisted open-incident row rather than in-memory
// status, alerting survives process restarts and partition handoffs, and a
// monitor that is unhealthy from its very first check still alerts.
type incidentTransition int

const (
	// transitionNone: nothing changed (still healthy, still down at the same
	// severity, or a DB error prevented recording — in which case the next
	// check retries and alerts then).
	transitionNone incidentTransition = iota
	// transitionOpened: the monitor just became unhealthy.
	transitionOpened
	// transitionEscalated: an open incident got worse (DEGRADED -> DOWN).
	transitionEscalated
	// transitionResolved: the monitor recovered.
	transitionResolved
)

// incidentTracker maintains the incidents table for one monitor. Each monitor
// job owns exactly one tracker (a monitor maps to exactly one scheduler
// goroutine/owner, so there are no concurrent writers for the same monitor).
//
// The open-incident row is loaded from Postgres once on the first check and
// cached afterwards, so the steady state — healthy monitor, no open incident —
// costs zero incident queries per check:
//
//   - unhealthy result + no open incident  -> open a new incident   (Opened)
//   - unhealthy result + open incident      -> update message/status (Escalated if worse)
//   - healthy result   + open incident      -> resolve it            (Resolved)
//   - healthy result   + no open incident   -> no-op                 (None)
type incidentTracker struct {
	dbc        *database.Client
	loaded     bool
	openID     string    // "" when no incident is open
	openStatus db.Status // status of the open incident, valid when openID != ""
}

func newIncidentTracker(dbc *database.Client) *incidentTracker {
	return &incidentTracker{dbc: dbc}
}

func (t *incidentTracker) record(ctx context.Context, monitorID string, r *models.CheckResult) incidentTransition {
	if !t.loaded {
		open, err := t.dbc.Prisma.Incident.FindFirst(
			db.Incident.MonitorID.Equals(monitorID),
			db.Incident.ResolvedAt.IsNull(),
		).OrderBy(
			db.Incident.StartedAt.Order(db.DESC),
		).Exec(ctx)
		if err != nil && !db.IsErrNotFound(err) {
			log.Printf("Failed to look up open incident for %s: %v", monitorID, err)
			return transitionNone // stay unloaded; retry on the next check
		}
		if err == nil && open != nil {
			t.openID = open.ID
			t.openStatus = open.Status
		}
		t.loaded = true
	}

	hasOpen := t.openID != ""
	healthy := r.Status == models.StatusUP

	switch {
	case !healthy && !hasOpen:
		params := []db.IncidentSetParam{
			db.Incident.Message.Set(r.Message),
		}
		if r.StatusCode != nil {
			params = append(params, db.Incident.StatusCode.Set(*r.StatusCode))
		}
		created, err := t.dbc.Prisma.Incident.CreateOne(
			db.Incident.Monitor.Link(db.Monitor.ID.Equals(monitorID)),
			db.Incident.Status.Set(db.Status(r.Status)),
			params...,
		).Exec(ctx)
		if err != nil {
			log.Printf("Failed to open incident for %s: %v", monitorID, err)
			return transitionNone
		}
		t.openID = created.ID
		t.openStatus = created.Status
		return transitionOpened

	case !healthy && hasOpen:
		newStatus := worstStatus(models.Status(t.openStatus), r.Status)
		params := []db.IncidentSetParam{
			db.Incident.Message.Set(r.Message),
			db.Incident.Status.Set(newStatus),
		}
		if r.StatusCode != nil {
			params = append(params, db.Incident.StatusCode.Set(*r.StatusCode))
		}
		if _, err := t.dbc.Prisma.Incident.FindUnique(
			db.Incident.ID.Equals(t.openID),
		).Update(params...).Exec(ctx); err != nil {
			log.Printf("Failed to update incident %s: %v", t.openID, err)
			return transitionNone
		}
		escalated := newStatus != t.openStatus
		t.openStatus = newStatus
		if escalated {
			return transitionEscalated
		}
		return transitionNone

	case healthy && hasOpen:
		if _, err := t.dbc.Prisma.Incident.FindUnique(
			db.Incident.ID.Equals(t.openID),
		).Update(
			db.Incident.ResolvedAt.Set(time.Now()),
		).Exec(ctx); err != nil {
			log.Printf("Failed to resolve incident %s: %v", t.openID, err)
			return transitionNone
		}
		t.openID = ""
		return transitionResolved
	}

	return transitionNone
}

// worstStatus keeps the more severe of two unhealthy statuses (DOWN > DEGRADED)
// so an incident that ever went fully DOWN is recorded as DOWN.
func worstStatus(a, b models.Status) db.Status {
	if a == models.StatusDOWN || b == models.StatusDOWN {
		return db.Status(models.StatusDOWN)
	}
	return db.Status(b)
}

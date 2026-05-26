package statestore

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"upguardly-backend/internal/models"
)

type SQLiteStore struct {
	db *sql.DB
	mu sync.RWMutex
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	store := &SQLiteStore{db: db}
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

func (s *SQLiteStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS monitor_status (
		monitor_id TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS instance_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		instance_id TEXT NOT NULL,
		partitions TEXT NOT NULL,
		last_sync_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_monitor_status_updated_at ON monitor_status(updated_at);
	`

	_, err := s.db.Exec(schema)
	return err
}

func (s *SQLiteStore) GetLastStatus(monitorID string) (models.Status, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var status string
	err := s.db.QueryRow(
		"SELECT status FROM monitor_status WHERE monitor_id = ?",
		monitorID,
	).Scan(&status)

	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to get status: %w", err)
	}

	return models.Status(status), true, nil
}

func (s *SQLiteStore) SetLastStatus(monitorID string, status models.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO monitor_status (monitor_id, status, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(monitor_id) DO UPDATE SET
			status = excluded.status,
			updated_at = excluded.updated_at
	`, monitorID, string(status), time.Now())

	if err != nil {
		return fmt.Errorf("failed to set status: %w", err)
	}
	return nil
}

func (s *SQLiteStore) GetAllStatuses() (map[string]models.Status, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT monitor_id, status FROM monitor_status")
	if err != nil {
		return nil, fmt.Errorf("failed to query statuses: %w", err)
	}
	defer rows.Close()

	statuses := make(map[string]models.Status)
	for rows.Next() {
		var monitorID, status string
		if err := rows.Scan(&monitorID, &status); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		statuses[monitorID] = models.Status(status)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return statuses, nil
}

func (s *SQLiteStore) CleanupStale(activeIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(activeIDs) == 0 {
		_, err := s.db.Exec("DELETE FROM monitor_status")
		return err
	}

	placeholders := make([]byte, 0, len(activeIDs)*2)
	args := make([]interface{}, len(activeIDs))
	for i, id := range activeIDs {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}

	query := fmt.Sprintf(
		"DELETE FROM monitor_status WHERE monitor_id NOT IN (%s)",
		string(placeholders),
	)
	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("failed to cleanup stale statuses: %w", err)
	}
	return nil
}

func (s *SQLiteStore) SaveInstanceState(instanceID string, partitions []int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	partitionsJSON, err := json.Marshal(partitions)
	if err != nil {
		return fmt.Errorf("failed to marshal partitions: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO instance_state (id, instance_id, partitions, last_sync_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			instance_id = excluded.instance_id,
			partitions = excluded.partitions,
			last_sync_at = excluded.last_sync_at
	`, instanceID, string(partitionsJSON), time.Now())

	if err != nil {
		return fmt.Errorf("failed to save instance state: %w", err)
	}
	return nil
}

type InstanceState struct {
	InstanceID string
	Partitions []int
	LastSyncAt time.Time
}

func (s *SQLiteStore) GetInstanceState() (*InstanceState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var instanceID, partitionsJSON string
	var lastSyncAt time.Time

	err := s.db.QueryRow(`
		SELECT instance_id, partitions, last_sync_at
		FROM instance_state
		WHERE id = 1
	`).Scan(&instanceID, &partitionsJSON, &lastSyncAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get instance state: %w", err)
	}

	var partitions []int
	if err := json.Unmarshal([]byte(partitionsJSON), &partitions); err != nil {
		return nil, fmt.Errorf("failed to unmarshal partitions: %w", err)
	}

	return &InstanceState{
		InstanceID: instanceID,
		Partitions: partitions,
		LastSyncAt: lastSyncAt,
	}, nil
}

func (s *SQLiteStore) DeleteMonitorStatus(monitorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM monitor_status WHERE monitor_id = ?", monitorID)
	if err != nil {
		return fmt.Errorf("failed to delete monitor status: %w", err)
	}
	return nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

package bun

// Insert-marshaling tests for columns whose zero value is meaningful.
//
// bun substitutes the SQL DEFAULT keyword for any zero-valued field carrying a
// `default:` tag (see marshalsToDefault in bun's query_insert.go). Every
// `enabled` column here is NOT NULL DEFAULT true, so a `default:true` tag on
// the field turned an explicit false into an enabled row: the monitor detail
// page adds an integration by creating it disabled account-wide and then
// opting the one monitor in, and the flipped flag made that integration alert
// for every monitor instead.
//
// These build the query without a database on purpose — CI runs `go test`
// before it applies migrations and sets no BUN_TEST_DATABASE_URL, so the
// DB-backed twin (TestCreatePathsPersistFalseBooleans) always skips there.

import (
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

func TestInsertsSendExplicitFalse(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	now := time.Now()

	cases := []struct {
		name  string
		query *bun.InsertQuery
	}{
		{
			// CreateMonitor: a monitor created paused must stay paused.
			name: "monitor",
			query: db.NewInsert().Model(&Monitor{
				ID: "m-1", UserID: "u-1", Name: "n", Type: "HTTP", Target: "http://example.test",
				Timeout: 30, Enabled: false, Regions: []string{"ca-east"}, UpdatedAt: now,
			}),
		},
		{
			// CreateNotificationChannel: the per-monitor integration flow.
			name: "notification channel",
			query: db.NewInsert().Model(&NotificationChannel{
				ID: "ch-1", UserID: "u-1", Channel: "DISCORD", Target: "https://discord.test/hook",
				Enabled: false, UpdatedAt: now,
			}),
		},
		{
			name: "monitor channel setting",
			query: db.NewInsert().Model(&MonitorChannelSetting{
				ID: "mcs-1", MonitorID: "m-1", NotificationChannelID: "ch-1",
				Enabled: false, UpdatedAt: now,
			}),
		},
		{
			name: "alert",
			query: db.NewInsert().Model(&Alert{
				ID: "a-1", MonitorID: "m-1", Channel: "EMAIL", Target: "on@call.test", Enabled: false,
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql := tc.query.ExcludeColumn("created_at").String()
			got := insertedValue(t, sql, "enabled")
			if !strings.EqualFold(got, "false") {
				t.Errorf("enabled=false was written as %s, want FALSE\n%s", got, sql)
			}
		})
	}
}

// insertedValue returns the VALUES entry an INSERT assigns to column, so a
// test can tell a literal apart from the DEFAULT keyword. Both lists are split
// on top-level commas — string literals may contain their own (array literals
// such as '{"ca-east","us-west"}').
func insertedValue(t *testing.T, sql, column string) string {
	t.Helper()

	columns := splitList(t, sql, strings.Index(sql, "("))
	values := splitList(t, sql, strings.Index(sql, " VALUES (")+len(" VALUES "))

	if len(columns) != len(values) {
		t.Fatalf("%d columns but %d values in\n%s", len(columns), len(values), sql)
	}
	for i, c := range columns {
		if c == `"`+column+`"` {
			return values[i]
		}
	}
	t.Fatalf("column %q not in\n%s", column, sql)
	return ""
}

// splitList splits the parenthesized list starting at open into its top-level
// comma-separated entries, ignoring commas and parentheses inside '…' literals.
func splitList(t *testing.T, sql string, open int) []string {
	t.Helper()
	if open < 0 || sql[open] != '(' {
		t.Fatalf("no list at offset %d in\n%s", open, sql)
	}

	var (
		entries []string
		start   = open + 1
		depth   = 1
		quoted  bool
	)
	for i := start; i < len(sql); i++ {
		switch {
		case quoted:
			if sql[i] == '\'' {
				quoted = false
			}
		case sql[i] == '\'':
			quoted = true
		case sql[i] == '(':
			depth++
		case sql[i] == ')':
			if depth--; depth == 0 {
				return append(entries, strings.TrimSpace(sql[start:i]))
			}
		case sql[i] == ',' && depth == 1:
			entries = append(entries, strings.TrimSpace(sql[start:i]))
			start = i + 1
		}
	}
	t.Fatalf("unterminated list in\n%s", sql)
	return nil
}

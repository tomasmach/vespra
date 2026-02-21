package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// newTestStore opens an in-memory SQLite logstore for testing.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), migrationSQL); err != nil {
		db.Close()
		t.Fatalf("run migration: %v", err)
	}
	s := &Store{db: db}
	t.Cleanup(func() { db.Close() })
	return s
}

func TestWriteAndList(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.write(ctx, time.Now(), "INFO", "hello world", "srv1", "chan1", "")

	rows, total, err := s.List(ctx, "srv1", "", 10, 0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Msg != "hello world" {
		t.Errorf("expected msg %q, got %q", "hello world", rows[0].Msg)
	}
	if rows[0].Level != "INFO" {
		t.Errorf("expected level %q, got %q", "INFO", rows[0].Level)
	}
	if rows[0].ServerID != "srv1" {
		t.Errorf("expected server_id %q, got %q", "srv1", rows[0].ServerID)
	}
	if rows[0].ChannelID != "chan1" {
		t.Errorf("expected channel_id %q, got %q", "chan1", rows[0].ChannelID)
	}
}

func TestListFiltersByServerID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.write(ctx, time.Now(), "INFO", "msg for srv1", "srv1", "", "")
	s.write(ctx, time.Now(), "INFO", "msg for srv2", "srv2", "", "")

	rowsSrv1, total1, err := s.List(ctx, "srv1", "", 10, 0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if total1 != 1 {
		t.Errorf("expected 1 row for srv1, got %d", total1)
	}
	for _, r := range rowsSrv1 {
		if r.ServerID != "srv1" {
			t.Errorf("got row with unexpected server_id %q", r.ServerID)
		}
	}

	rowsSrv2, total2, err := s.List(ctx, "srv2", "", 10, 0)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if total2 != 1 {
		t.Errorf("expected 1 row for srv2, got %d", total2)
	}
	for _, r := range rowsSrv2 {
		if r.ServerID != "srv2" {
			t.Errorf("got row with unexpected server_id %q", r.ServerID)
		}
	}
}

func TestListFiltersByLevel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.write(ctx, time.Now(), "DEBUG", "debug msg", "srv1", "", "")
	s.write(ctx, time.Now(), "INFO", "info msg", "srv1", "", "")
	s.write(ctx, time.Now(), "WARN", "warn msg", "srv1", "", "")
	s.write(ctx, time.Now(), "ERROR", "error msg", "srv1", "", "")

	// "warn" level should return WARN and ERROR only
	rows, total, err := s.List(ctx, "srv1", "warn", 10, 0)
	if err != nil {
		t.Fatalf("List(level=warn) error: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 rows for level>=warn, got %d", total)
	}
	for _, r := range rows {
		if r.Level != "WARN" && r.Level != "ERROR" {
			t.Errorf("unexpected level %q in warn-filtered results", r.Level)
		}
	}

	// "error" level should return ERROR only
	rows, total, err = s.List(ctx, "srv1", "error", 10, 0)
	if err != nil {
		t.Fatalf("List(level=error) error: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 row for level>=error, got %d", total)
	}
	if len(rows) > 0 && rows[0].Level != "ERROR" {
		t.Errorf("expected ERROR level, got %q", rows[0].Level)
	}

	// "debug" level should return all 4
	rows, total, err = s.List(ctx, "srv1", "debug", 10, 0)
	if err != nil {
		t.Fatalf("List(level=debug) error: %v", err)
	}
	if total != 4 {
		t.Errorf("expected 4 rows for level>=debug, got %d", total)
	}
	_ = rows
}

func TestListDefaultLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := range 5 {
		s.write(ctx, time.Now(), "INFO", fmt.Sprintf("msg %d", i), "srv1", "", "")
	}

	// limit=0 should default to 100
	rows, total, err := s.List(ctx, "srv1", "", 0, 0)
	if err != nil {
		t.Fatalf("List(limit=0) error: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total=5, got %d", total)
	}
	if len(rows) != 5 {
		t.Errorf("expected 5 rows, got %d", len(rows))
	}
}

func TestPruneKeepsOtherServers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert 10001 rows for srv1 (exceeds the 10000 row limit)
	const overLimit = 10001
	for i := range overLimit {
		s.write(ctx, time.Now(), "INFO", fmt.Sprintf("srv1 msg %d", i), "srv1", "", "")
	}

	// Insert 5 rows for srv2
	const srv2Count = 5
	for i := range srv2Count {
		s.write(ctx, time.Now(), "INFO", fmt.Sprintf("srv2 msg %d", i), "srv2", "", "")
	}

	// Explicitly prune
	s.prune(ctx)

	// srv1 should now have at most 10000 rows
	_, totalSrv1, err := s.List(ctx, "srv1", "", 1, 0)
	if err != nil {
		t.Fatalf("List(srv1) error: %v", err)
	}
	if totalSrv1 > 10000 {
		t.Errorf("expected srv1 rows <= 10000 after prune, got %d", totalSrv1)
	}

	// srv2 should still have all 5 rows
	_, totalSrv2, err := s.List(ctx, "srv2", "", 1, 0)
	if err != nil {
		t.Fatalf("List(srv2) error: %v", err)
	}
	if totalSrv2 != srv2Count {
		t.Errorf("expected srv2 rows=%d after prune, got %d", srv2Count, totalSrv2)
	}
}

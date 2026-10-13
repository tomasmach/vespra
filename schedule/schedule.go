// Package schedule persists scheduled tasks and dispatches them when due.
package schedule

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/robfig/cron/v3"
)

const migrationSQL = `
CREATE TABLE IF NOT EXISTS tasks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id  TEXT NOT NULL,
    channel_id TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    prompt     TEXT NOT NULL,
    at         TEXT NOT NULL,
    cron       TEXT NOT NULL,
    next_run   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_next_run ON tasks(next_run);
CREATE INDEX IF NOT EXISTS idx_tasks_server ON tasks(server_id);
`

// oneShotRetry is how long a fired one-shot task waits before it fires again
// when the channel agent has not run it.
const oneShotRetry = 10 * time.Minute

type Task struct {
	ID        int64
	ServerID  string
	ChannelID string
	UserID    string
	Prompt    string
	At        string
	Cron      string
	NextRun   time.Time
}

type Store struct {
	db *sql.DB
}

func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create schedule db dir: %w", err)
	}
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open schedule db: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), migrationSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("schedule db migration: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(ctx context.Context, serverID, channelID, userID, prompt, at, cronSpec string) (Task, error) {
	task := Task{ServerID: serverID, ChannelID: channelID, UserID: userID, Prompt: prompt, At: at, Cron: cronSpec}
	if strings.TrimSpace(prompt) == "" {
		return task, fmt.Errorf("prompt is required")
	}
	if (at == "") == (cronSpec == "") {
		return task, fmt.Errorf("provide exactly one of at or cron")
	}
	now := time.Now().In(time.Local)
	if at != "" {
		next, err := time.ParseInLocation("2006-01-02 15:04", at, time.Local)
		if err != nil {
			return task, fmt.Errorf("at must use YYYY-MM-DD HH:MM local time")
		}
		if !next.After(now) {
			return task, fmt.Errorf("at must be in the future")
		}
		task.NextRun = next
	} else {
		// Local time only. robfig/cron also panics on a TZ= prefix not followed by a space.
		if len(strings.Fields(cronSpec)) != 5 || strings.Contains(cronSpec, "TZ=") {
			return task, fmt.Errorf("cron must have five fields in local time")
		}
		parsed, err := cron.ParseStandard(cronSpec)
		if err != nil {
			return task, fmt.Errorf("invalid cron: %w", err)
		}
		task.NextRun = parsed.Next(now)
		if task.NextRun.IsZero() {
			return task, fmt.Errorf("cron has no next occurrence")
		}
		for run, i := task.NextRun, 0; i < 100; i++ {
			next := parsed.Next(run)
			if next.Sub(run) < 15*time.Minute {
				return task, fmt.Errorf("cron runs must be at least 15 minutes apart")
			}
			run = next
		}
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (server_id, channel_id, user_id, prompt, at, cron, next_run)
		SELECT ?, ?, ?, ?, ?, ?, ? WHERE (SELECT COUNT(*) FROM tasks WHERE server_id = ?) < 20`,
		serverID, channelID, userID, prompt, at, cronSpec, task.NextRun.Unix(), serverID)
	if err != nil {
		return task, fmt.Errorf("create scheduled task: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return task, fmt.Errorf("count created tasks: %w", err)
	}
	if count == 0 {
		return task, fmt.Errorf("server limit reached: cancel a pending task first (maximum 20)")
	}
	task.ID, err = result.LastInsertId()
	return task, err
}

func (s *Store) List(ctx context.Context, serverID string) ([]Task, error) {
	return s.query(ctx, "server_id = ?", serverID)
}

func (s *Store) Due(ctx context.Context, now time.Time) ([]Task, error) {
	return s.query(ctx, "next_run <= ?", now.Unix())
}

func (s *Store) query(ctx context.Context, where string, arg any) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, server_id, channel_id, user_id, prompt, at, cron, next_run FROM tasks WHERE `+where+` ORDER BY next_run, id`, arg)
	if err != nil {
		return nil, fmt.Errorf("query scheduled tasks: %w", err)
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var task Task
		var next int64
		if err := rows.Scan(&task.ID, &task.ServerID, &task.ChannelID, &task.UserID, &task.Prompt, &task.At, &task.Cron, &next); err != nil {
			return nil, fmt.Errorf("scan scheduled task: %w", err)
		}
		task.NextRun = time.Unix(next, 0).In(time.Local)
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *Store) Cancel(ctx context.Context, serverID string, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE server_id = ? AND id = ?`, serverID, id)
	if err != nil {
		return fmt.Errorf("cancel scheduled task: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count cancelled tasks: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("task not found on this server")
	}
	return nil
}

// Exists reports whether a task is still pending, i.e. not cancelled or done.
func (s *Store) Exists(ctx context.Context, id int64) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = ?`, id).Scan(&n); err != nil {
		return false, fmt.Errorf("check scheduled task: %w", err)
	}
	return n > 0, nil
}

// Finish moves a fired task to its next run. A one-shot task gets a retry time
// instead of being deleted: the channel agent deletes it once it has run, so a
// restart while it waits in the agent's queue does not lose it.
func (s *Store) Finish(ctx context.Context, task Task, now time.Time) error {
	next := now.Add(oneShotRetry)
	if task.Cron != "" {
		parsed, err := cron.ParseStandard(task.Cron)
		if err != nil {
			return fmt.Errorf("parse stored cron: %w", err)
		}
		next = parsed.Next(now.In(time.Local))
	}
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET next_run = ? WHERE id = ? AND server_id = ?`, next.Unix(), task.ID, task.ServerID)
	if err != nil {
		return fmt.Errorf("advance scheduled task: %w", err)
	}
	return nil
}

func (s *Store) Run(ctx context.Context, fire func(context.Context, Task) bool) {
	now := time.Now()
	tasks, err := s.Due(ctx, now)
	if err != nil {
		slog.Warn("load overdue scheduled tasks", "error", err)
		return
	}
	for _, task := range tasks {
		if task.Cron != "" {
			if err := s.Finish(ctx, task, now); err != nil {
				slog.Warn("skip overdue recurring task", "error", err, "task_id", task.ID)
			}
		}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		tasks, err := s.Due(ctx, time.Now())
		if err != nil {
			slog.Warn("load due scheduled tasks", "error", err)
		}
		for _, task := range tasks {
			keep := fire(ctx, task)
			if ctx.Err() != nil {
				return
			}
			if keep {
				err = s.Finish(ctx, task, time.Now())
			} else {
				err = s.Cancel(ctx, task.ServerID, task.ID)
			}
			if err != nil {
				slog.Warn("finish scheduled task", "error", err, "task_id", task.ID)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

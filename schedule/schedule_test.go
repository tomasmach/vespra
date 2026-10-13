package schedule

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func createTask(t *testing.T, store *Store, serverID, cronSpec string) Task {
	t.Helper()
	at := ""
	if cronSpec == "" {
		at = time.Now().Add(time.Hour).Format("2006-01-02 15:04")
	}
	task, err := store.Create(context.Background(), serverID, "channel", "user", "remind <@user> about the tram", at, cronSpec)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func setDue(t *testing.T, store *Store, task Task, when time.Time) Task {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), `UPDATE tasks SET next_run = ? WHERE id = ?`, when.Unix(), task.ID); err != nil {
		t.Fatal(err)
	}
	task.NextRun = when
	return task
}

func TestStorePersistsAndScopesTasks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schedule.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task := createTask(t, store, "server1", "")
	createTask(t, store, "server2", "0 9 * * *")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listed, err := store.List(ctx, "server1")
	if err != nil || len(listed) != 1 {
		t.Fatalf("list after restart: %v, %v", listed, err)
	}
	if listed[0] != task {
		t.Errorf("stored task = %+v, want %+v", listed[0], task)
	}
	if err := store.Cancel(ctx, "server2", task.ID); err == nil {
		t.Error("cross-server cancellation succeeded")
	}
	if err := store.Cancel(ctx, "server1", task.ID); err != nil {
		t.Fatal(err)
	}
	listed, err = store.List(ctx, "server1")
	if err != nil || len(listed) != 0 {
		t.Fatalf("list after cancel: %v, %v", listed, err)
	}
	listed, err = store.List(ctx, "server2")
	if err != nil || len(listed) != 1 {
		t.Fatalf("other server affected: %v, %v", listed, err)
	}
}

func TestDueAndFinish(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	oneShot := setDue(t, store, createTask(t, store, "server", ""), now.Add(-time.Hour))
	recurring := setDue(t, store, createTask(t, store, "server", "*/15 * * * *"), now.Add(-time.Minute))
	future := createTask(t, store, "server", "")
	due, err := store.Due(ctx, now)
	if err != nil || len(due) != 2 || due[0].ID != oneShot.ID || due[1].ID != recurring.ID {
		t.Fatalf("due: %+v, %v", due, err)
	}
	for _, task := range due {
		if err := store.Finish(ctx, task, now); err != nil {
			t.Fatal(err)
		}
	}
	if due, err := store.Due(ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("still due after firing: %+v, %v", due, err)
	}
	listed, err := store.List(ctx, "server")
	if err != nil || len(listed) != 3 {
		t.Fatalf("after firing: %+v, %v", listed, err)
	}
	parsed, err := cron.ParseStandard(recurring.Cron)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range listed {
		if task.ID == oneShot.ID && !task.NextRun.Equal(now.Add(oneShotRetry)) {
			t.Errorf("one-shot retry = %s, want %s", task.NextRun, now.Add(oneShotRetry))
		}
		if task.ID == recurring.ID && !task.NextRun.Equal(parsed.Next(now.In(time.Local))) {
			t.Errorf("next recurring run = %s", task.NextRun)
		}
		if task.ID == future.ID && task.NextRun != future.NextRun {
			t.Error("future task changed")
		}
	}
}

func TestSchedulerRestartSkipsRecurringAndFiresOverdueOneShot(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().Truncate(time.Second)
	oneShot := setDue(t, store, createTask(t, store, "server", ""), now.Add(-48*time.Hour))
	setDue(t, store, createTask(t, store, "server", "*/15 * * * *"), now.Add(-48*time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan Task, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.Run(ctx, func(ctx context.Context, task Task) bool {
			fired <- task
			return true
		})
	}()
	select {
	case task := <-fired:
		if task.ID != oneShot.ID || !task.NextRun.Equal(oneShot.NextRun) {
			t.Errorf("fired %+v, want overdue one-shot %+v", task, oneShot)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overdue one-shot did not fire")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		listed, err := store.List(context.Background(), "server")
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) == 2 && listed[0].NextRun.After(now) && listed[1].NextRun.After(now) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduler did not finish tasks: %+v", listed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	select {
	case task := <-fired:
		t.Errorf("overdue recurring task fired: %+v", task)
	default:
	}
}

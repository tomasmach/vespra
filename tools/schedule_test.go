package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tomasmach/vespra/schedule"
	"github.com/tomasmach/vespra/tools"
)

func TestScheduleToolsValidationAndServerScope(t *testing.T) {
	ctx := context.Background()
	store, err := schedule.Open(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reg := tools.NewRegistry()
	tools.RegisterScheduleTools(reg, store, "server1", "channel1", "user1")
	future := time.Now().Add(time.Hour).Format("2006-01-02 15:04")
	for _, tc := range []struct {
		name string
		at   string
		cron string
		want string
	}{
		{"past", "2000-01-01 16:06", "", "future"},
		{"bad time", "tomorrow", "", "YYYY-MM-DD HH:MM"},
		{"bad cron", "", "bad cron", "five fields"},
		{"invalid field", "", "90 * * * *", "invalid cron"},
		{"both", future, "0 9 * * *", "exactly one"},
		{"neither", "", "", "exactly one"},
		{"every minute", "", "* * * * *", "15 minutes"},
		{"every ten minutes", "", "*/10 * * * *", "15 minutes"},
		{"later close pair", "", "0,40,50 * * * *", "15 minutes"},
		{"midnight close pair", "", "0,59 0,23 * * *", "15 minutes"},
		{"descriptor", "", "@every 1h", "five fields"},
		{"timezone without schedule", "", "TZ=UTC", "five fields"},
		{"timezone override", "", "CRON_TZ=UTC 0 9 * * *", "five fields"},
		{"impossible date", "", "0 9 31 2 *", "no next occurrence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"prompt": "remind me", "at": tc.at, "cron": tc.cron})
			if err != nil {
				t.Fatal(err)
			}
			result, err := reg.Dispatch(ctx, "schedule_task", args)
			if err != nil || !strings.Contains(result, tc.want) {
				t.Fatalf("result %q, %v; want %q", result, err, tc.want)
			}
		})
	}
	for i := 0; i < 20; i++ {
		args := fmt.Sprintf(`{"prompt":"tram %d","at":%q}`, i, future)
		if i == 0 {
			args = `{"prompt":"tram 0","cron":"*/15 * * * *"}`
		}
		if i == 1 {
			args = `{"prompt":"tram 1","cron":"0,50 9 * * *"}`
		}
		if i == 2 {
			args = `{"prompt":"tram 2","cron":"0,59 0,23 1 * *"}`
		}
		result, err := reg.Dispatch(ctx, "schedule_task", []byte(args))
		if err != nil || !strings.Contains(result, "Next run:") {
			t.Fatalf("create %d: %q, %v", i, result, err)
		}
	}
	result, err := reg.Dispatch(ctx, "schedule_task", []byte(fmt.Sprintf(`{"prompt":"21st","at":%q}`, future)))
	if err != nil || !strings.Contains(result, "maximum 20") {
		t.Fatalf("limit: %q, %v", result, err)
	}
	listed, err := store.List(ctx, "server1")
	if err != nil || len(listed) != 20 {
		t.Fatalf("list: %v, %v", listed, err)
	}
	if listed[0].ChannelID != "channel1" || listed[0].UserID != "user1" {
		t.Fatalf("metadata: %+v", listed[0])
	}
	result, err = reg.Dispatch(ctx, "list_tasks", []byte(`{}`))
	if err != nil || !strings.Contains(result, "channel1") || !strings.Contains(result, "tram") || !strings.Contains(result, "cron") || !strings.Contains(result, "UTC") {
		t.Fatalf("tool list: %q, %v", result, err)
	}
	other := tools.NewRegistry()
	tools.RegisterScheduleTools(other, store, "server2", "channel2", "user2")
	result, err = other.Dispatch(ctx, "cancel_task", []byte(fmt.Sprintf(`{"task_id":%d}`, listed[0].ID)))
	if err != nil || !strings.Contains(result, "not found") {
		t.Fatalf("cross-server cancel: %q, %v", result, err)
	}
	result, err = other.Dispatch(ctx, "list_tasks", []byte(`{}`))
	if err != nil || result != "No pending tasks." {
		t.Fatalf("cross-server list: %q, %v", result, err)
	}
	result, err = other.Dispatch(ctx, "schedule_task", []byte(fmt.Sprintf(`{"prompt":"separate limit","at":%q}`, future)))
	if err != nil || !strings.Contains(result, "Next run:") {
		t.Fatalf("separate limit: %q, %v", result, err)
	}
	result, err = reg.Dispatch(ctx, "cancel_task", []byte(fmt.Sprintf(`{"task_id":%d}`, listed[0].ID)))
	if err != nil || result != "Task cancelled." {
		t.Fatalf("cancel: %q, %v", result, err)
	}
	result, err = reg.Dispatch(ctx, "schedule_task", []byte(fmt.Sprintf(`{"prompt":"replacement","at":%q}`, future)))
	if err != nil || !strings.Contains(result, "Next run:") {
		t.Fatalf("replacement: %q, %v", result, err)
	}
}

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tomasmach/vespra/schedule"
)

func RegisterScheduleTools(reg *Registry, store *schedule.Store, serverID, channelID, userID string) {
	reg.Register(&scheduleTaskTool{store: store, serverID: serverID, channelID: channelID, userID: userID})
	RegisterTaskManagementTools(reg, store, serverID)
}

// RegisterTaskManagementTools registers list_tasks and cancel_task without
// schedule_task, for scheduled task turns that must not create more tasks.
func RegisterTaskManagementTools(reg *Registry, store *schedule.Store, serverID string) {
	reg.Register(&listTasksTool{store: store, serverID: serverID})
	reg.Register(&cancelTaskTool{store: store, serverID: serverID})
}

type scheduleTaskTool struct {
	store     *schedule.Store
	serverID  string
	channelID string
	userID    string
}

func (t *scheduleTaskTool) Name() string { return "schedule_task" }
func (t *scheduleTaskTool) Description() string {
	return "Schedule an instruction to carry out later in this channel. Provide prompt and exactly one of at or cron, in local time. If the user gives an event time but no reminder time, pick a sensible time before the event. Confirm the reminder time in your reply. Maximum 20 pending tasks per server; recurring runs must be at least 15 minutes apart."
}
func (t *scheduleTaskTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","description":"Instruction to carry out when the task fires."},"at":{"type":"string","description":"One-shot local time, YYYY-MM-DD HH:MM."},"cron":{"type":"string","description":"Recurring local time, standard five-field cron."}},"required":["prompt"]}`)
}
func (t *scheduleTaskTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Prompt string `json:"prompt"`
		At     string `json:"at"`
		Cron   string `json:"cron"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	task, err := t.store.Create(ctx, t.serverID, t.channelID, t.userID, p.Prompt, p.At, p.Cron)
	if err != nil {
		return err.Error(), nil
	}
	return fmt.Sprintf("Task %d scheduled. Next run: %s.", task.ID, task.NextRun.Format("2006-01-02 15:04 MST (UTC-07:00)")), nil
}

type listTasksTool struct {
	store    *schedule.Store
	serverID string
}

func (t *listTasksTool) Name() string        { return "list_tasks" }
func (t *listTasksTool) Description() string { return "List pending scheduled tasks on this server." }
func (t *listTasksTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *listTasksTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	tasks, err := t.store.List(ctx, t.serverID)
	if err != nil {
		return "", err
	}
	if len(tasks) == 0 {
		return "No pending tasks.", nil
	}
	var sb strings.Builder
	for _, task := range tasks {
		when := task.At
		if task.Cron != "" {
			when = "cron " + task.Cron
		}
		fmt.Fprintf(&sb, "[%d] channel %s; schedule %s; next run %s; %s\n", task.ID, task.ChannelID, when, task.NextRun.Format("2006-01-02 15:04 MST (UTC-07:00)"), task.Prompt)
	}
	return sb.String(), nil
}

type cancelTaskTool struct {
	store    *schedule.Store
	serverID string
}

func (t *cancelTaskTool) Name() string        { return "cancel_task" }
func (t *cancelTaskTool) Description() string { return "Cancel a scheduled task by ID on this server." }
func (t *cancelTaskTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"integer"}},"required":["task_id"]}`)
}
func (t *cancelTaskTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		TaskID int64 `json:"task_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.store.Cancel(ctx, t.serverID, p.TaskID); err != nil {
		return err.Error(), nil
	}
	return "Task cancelled.", nil
}

package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tomasmach/vespra/memory"
)

type visualMemorySaveTool struct {
	store           *memory.Store
	serverID        string
	channelID       string
	messageID       string
	sourceImageURLs []string
}

func (t *visualMemorySaveTool) Name() string { return ToolNameVisualMemorySave }
func (t *visualMemorySaveTool) Description() string {
	return "Save attached or replied-to images as long-term visual references. " +
		"Use this only when a user identifies the image subject with wording like 'this is Alice', 'this is him', 'remember this face', or similar. " +
		"Do not save random images or images without identity/reference intent."
}
func (t *visualMemorySaveTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"label": {"type": "string", "description": "Name or short label for the person/object shown."},
			"description": {"type": "string", "description": "Brief visual description useful for future recall."},
			"user_id": {"type": "string", "description": "Optional Discord user ID this visual memory is about."},
			"importance": {"type": "number", "description": "Importance score 0.0-1.0, default 0.5."}
		},
		"required": ["label"]
	}`)
}
func (t *visualMemorySaveTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Label       string   `json:"label"`
		Description string   `json:"description"`
		UserID      string   `json:"user_id"`
		Importance  *float64 `json:"importance"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Label) == "" {
		return "Error: label is required", nil
	}
	if len(t.sourceImageURLs) == 0 {
		return "Visual memory requires an attached or replied-to image.", nil
	}
	importance := 0.5
	if p.Importance != nil {
		importance = *p.Importance
	}

	var saved, existing int
	var ids []string
	for _, source := range t.sourceImageURLs {
		contentType, data, err := parseDataURL(source)
		if err != nil {
			return "", fmt.Errorf("parse source image: %w", err)
		}
		result, err := t.store.SaveVisual(ctx, memory.VisualSaveOptions{
			Label:       p.Label,
			Description: p.Description,
			ServerID:    t.serverID,
			UserID:      p.UserID,
			ChannelID:   t.channelID,
			MessageID:   t.messageID,
			ContentType: contentType,
			Data:        data,
			Importance:  importance,
		})
		if err != nil {
			return "", err
		}
		ids = append(ids, result.ID)
		if result.Status == memory.SaveStatusExists {
			existing++
		} else {
			saved++
		}
	}
	if saved == 0 && existing > 0 {
		return fmt.Sprintf("Visual memory already exists (id: %s)", strings.Join(ids, ", ")), nil
	}
	return fmt.Sprintf("Visual memory saved (id: %s)", strings.Join(ids, ", ")), nil
}

type visualMemoryRecallTool struct {
	store    *memory.Store
	serverID string
}

func (t *visualMemoryRecallTool) Name() string { return ToolNameVisualMemoryRecall }
func (t *visualMemoryRecallTool) Description() string {
	return "Search long-term visual references by person/object label or description. " +
		"Use this before generate_image when a requested image may involve someone or something visually remembered."
}
func (t *visualMemoryRecallTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Name, label, or visual description to search for."},
			"top_n": {"type": "integer", "description": "Max results to return."}
		},
		"required": ["query"]
	}`)
}
func (t *visualMemoryRecallTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		TopN  int    `json:"top_n"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	rows, err := t.store.RecallVisual(ctx, p.Query, t.serverID, p.TopN)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "No visual memories found.", nil
	}
	var sb strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&sb, "[%s] %s", row.ID, row.Label)
		if row.Description != "" {
			fmt.Fprintf(&sb, " — %s", row.Description)
		}
		fmt.Fprintln(&sb)
	}
	return sb.String(), nil
}

func parseDataURL(value string) (string, []byte, error) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.Contains(header, ";base64") {
		return "", nil, fmt.Errorf("expected base64 data URL")
	}
	contentType := strings.TrimPrefix(strings.TrimSuffix(header, ";base64"), "data:")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, err
	}
	return contentType, data, nil
}

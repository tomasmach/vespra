package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func msg(content string, attachments ...*discordgo.MessageAttachment) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content:     content,
			Author:      &discordgo.User{Username: "alice"},
			Attachments: attachments,
		},
	}
}

func attachment(contentType, url string) *discordgo.MessageAttachment {
	return &discordgo.MessageAttachment{ContentType: contentType, URL: url}
}

// imageServer starts a test HTTP server that serves fakeData for any request.
// Returns the server and a cleanup func.
func imageServer(t *testing.T, fakeData []byte) (*httptest.Server, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(fakeData) //nolint:errcheck
	}))
	return srv, srv.Close
}

func TestBuildUserMessageTextOnly(t *testing.T) {
	m := buildUserMessage(context.Background(), nil, msg("hello"), "", "")
	if m.Role != "user" {
		t.Errorf("expected role=user, got %q", m.Role)
	}
	if m.Content != "alice: hello" {
		t.Errorf("unexpected content: %q", m.Content)
	}
	if len(m.ContentParts) != 0 {
		t.Errorf("expected no content parts, got %d", len(m.ContentParts))
	}
}

func TestBuildUserMessageWithImage(t *testing.T) {
	fakeData := []byte("fake png bytes")
	srv, cleanup := imageServer(t, fakeData)
	defer cleanup()

	a := attachment("image/png", srv.URL+"/img.png")
	m := buildUserMessage(context.Background(), srv.Client(), msg("look", a), "", "")
	if len(m.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(m.ContentParts))
	}
	if m.ContentParts[0].Type != "text" {
		t.Errorf("expected first part type=text, got %q", m.ContentParts[0].Type)
	}
	if m.ContentParts[1].Type != "image_url" {
		t.Errorf("expected second part type=image_url, got %q", m.ContentParts[1].Type)
	}
	wantURL := fmt.Sprintf("data:image/png;base64,%s", base64.StdEncoding.EncodeToString(fakeData))
	if m.ContentParts[1].ImageURL.URL != wantURL {
		t.Errorf("unexpected image URL: %q", m.ContentParts[1].ImageURL.URL)
	}
}

func TestBuildUserMessageNonImageAttachmentIgnored(t *testing.T) {
	m := buildUserMessage(context.Background(), nil, msg("file", attachment("application/pdf", "https://cdn.example.com/doc.pdf")), "", "")
	if len(m.ContentParts) != 0 {
		t.Errorf("expected no content parts for non-image, got %d", len(m.ContentParts))
	}
	if m.Content != "alice: file" {
		t.Errorf("unexpected content: %q", m.Content)
	}
}

func TestBuildUserMessageMixedAttachments(t *testing.T) {
	fakeData := []byte("fake image bytes")
	srv, cleanup := imageServer(t, fakeData)
	defer cleanup()

	m := buildUserMessage(context.Background(), srv.Client(), msg("mixed",
		attachment("image/jpeg", srv.URL+"/photo.jpg"),
		attachment("application/pdf", "https://cdn.example.com/doc.pdf"),
		attachment("image/webp", srv.URL+"/pic.webp"),
	), "", "")
	if len(m.ContentParts) != 3 { // text + 2 images
		t.Fatalf("expected 3 content parts (text + 2 images), got %d", len(m.ContentParts))
	}
}

func TestBuildUserMessageImageDownloadFails(t *testing.T) {
	// Use an unreachable URL to simulate download failure
	a := attachment("image/png", "http://127.0.0.1:1") // nothing listening
	m := buildUserMessage(context.Background(), &http.Client{}, msg("look", a), "", "")
	// All images failed → falls back to plain text
	if len(m.ContentParts) != 0 {
		t.Errorf("expected no content parts when download fails, got %d", len(m.ContentParts))
	}
	if m.Content != "alice: look" {
		t.Errorf("unexpected content: %q", m.Content)
	}
}

func TestBuildUserMessageEmptyText(t *testing.T) {
	fakeData := []byte("fake gif bytes")
	srv, cleanup := imageServer(t, fakeData)
	defer cleanup()

	m := buildUserMessage(context.Background(), srv.Client(), msg("", attachment("image/gif", srv.URL+"/anim.gif")), "", "")
	if len(m.ContentParts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(m.ContentParts))
	}
	// text part should still carry the "alice: " prefix even with empty content
	if m.ContentParts[0].Text != "alice: " {
		t.Errorf("unexpected text part: %q", m.ContentParts[0].Text)
	}
	if !strings.HasPrefix(m.ContentParts[1].ImageURL.URL, "data:image/gif;base64,") {
		t.Errorf("expected base64 data URL, got: %q", m.ContentParts[1].ImageURL.URL)
	}
}

func TestHistoryUserContentNoReply(t *testing.T) {
	m := &discordgo.Message{
		Author:  &discordgo.User{Username: "alice"},
		Content: "hello",
	}
	got := historyUserContent(m, "", "")
	want := "alice: hello"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHistoryUserContentWithReply(t *testing.T) {
	m := &discordgo.Message{
		Author:  &discordgo.User{Username: "alice"},
		Content: "I agree",
		ReferencedMessage: &discordgo.Message{
			Author:  &discordgo.User{Username: "bob"},
			Content: "What do you think?",
		},
	}
	got := historyUserContent(m, "", "")
	want := `alice (replying to bob: "What do you think?"): I agree`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHistoryUserContentReplyWithImageOnly(t *testing.T) {
	m := &discordgo.Message{
		Author:  &discordgo.User{Username: "alice"},
		Content: "nice",
		ReferencedMessage: &discordgo.Message{
			Author:  &discordgo.User{Username: "bob"},
			Content: "",
			Attachments: []*discordgo.MessageAttachment{
				{ContentType: "image/png", URL: "https://cdn.example.com/img.png"},
			},
		},
	}
	got := historyUserContent(m, "", "")
	want := `alice (replying to bob: "[image]"): nice`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHistoryUserContentReplyNoAuthor(t *testing.T) {
	// ReferencedMessage exists but author is nil (deleted user) — should not panic
	m := &discordgo.Message{
		Author:            &discordgo.User{Username: "alice"},
		Content:           "hello",
		ReferencedMessage: &discordgo.Message{},
	}
	got := historyUserContent(m, "", "")
	want := "alice: hello"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildUserMessageReferencedImage(t *testing.T) {
	fakeData := []byte("fake ref image bytes")
	srv, cleanup := imageServer(t, fakeData)
	defer cleanup()

	refMsg := &discordgo.Message{
		Author:  &discordgo.User{Username: "bob"},
		Content: "",
		Attachments: []*discordgo.MessageAttachment{
			{ContentType: "image/jpeg", URL: srv.URL + "/ref.jpg"},
		},
	}
	m := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content:           "look at this",
			Author:            &discordgo.User{Username: "alice"},
			ReferencedMessage: refMsg,
		},
	}
	result := buildUserMessage(context.Background(), srv.Client(), m, "", "")
	if len(result.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts (text + ref image), got %d", len(result.ContentParts))
	}
	if result.ContentParts[0].Type != "text" {
		t.Errorf("expected first part type=text, got %q", result.ContentParts[0].Type)
	}
	if result.ContentParts[1].Type != "image_url" {
		t.Errorf("expected second part type=image_url, got %q", result.ContentParts[1].Type)
	}
	wantURL := fmt.Sprintf("data:image/jpeg;base64,%s", base64.StdEncoding.EncodeToString(fakeData))
	if result.ContentParts[1].ImageURL.URL != wantURL {
		t.Errorf("unexpected image URL: %q", result.ContentParts[1].ImageURL.URL)
	}
}

// msgAt creates a MessageCreate with the given username, content, and timestamp.
func msgAt(username, content string, ts time.Time) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content:   content,
			Author:    &discordgo.User{Username: username},
			Timestamp: ts,
		},
	}
}

func TestBuildCombinedContentHeader(t *testing.T) {
	now := time.Now()
	msgs := []*discordgo.MessageCreate{
		msgAt("alice", "hello", now),
		msgAt("bob", "world", now),
		msgAt("alice", "again", now),
	}
	got := buildCombinedContent(msgs, "", "")
	firstLine := strings.Split(got, "\n")[0]
	want := "[3 messages arrived rapidly in quick succession]"
	if firstLine != want {
		t.Errorf("got header %q, want %q", firstLine, want)
	}
}

func TestBuildCombinedContentMessageLines(t *testing.T) {
	now := time.Now()
	msgs := []*discordgo.MessageCreate{
		msgAt("alice", "hello", now),
		msgAt("bob", "world", now),
		msgAt("alice", "again", now),
	}
	got := buildCombinedContent(msgs, "", "")
	lines := strings.Split(got, "\n")
	// lines[0] = header, lines[1] = blank, lines[2..4] = messages
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %q", len(lines), got)
	}
	if lines[1] != "" {
		t.Errorf("expected blank separator line, got %q", lines[1])
	}
	if lines[2] != "alice: hello" {
		t.Errorf("expected %q, got %q", "alice: hello", lines[2])
	}
	if lines[3] != "bob: world" {
		t.Errorf("expected %q, got %q", "bob: world", lines[3])
	}
	if lines[4] != "alice: again" {
		t.Errorf("expected %q, got %q", "alice: again", lines[4])
	}
}

func TestBuildCombinedContentTimestampOnlyWhenGapAtLeastOneSecond(t *testing.T) {
	base := time.Now()
	msgs := []*discordgo.MessageCreate{
		msgAt("alice", "first", base),
		msgAt("bob", "quick", base.Add(500*time.Millisecond)),
		msgAt("alice", "later", base.Add(2*time.Second)),
	}
	got := buildCombinedContent(msgs, "", "")
	lines := strings.Split(got, "\n")
	// lines[2] = first message (no timestamp, it is the reference)
	// lines[3] = second message (gap < 1s, no timestamp)
	// lines[4] = third message (gap >= 1s, timestamp appended)
	if strings.Contains(lines[2], "(+") {
		t.Errorf("first message should not have timestamp, got %q", lines[2])
	}
	if strings.Contains(lines[3], "(+") {
		t.Errorf("second message (gap < 1s) should not have timestamp, got %q", lines[3])
	}
	if !strings.HasSuffix(lines[4], "(+2s)") {
		t.Errorf("third message (gap 2s) should end with (+2s), got %q", lines[4])
	}
}

func TestBuildCombinedContentSingleMessage(t *testing.T) {
	// handleMessages delegates to handleMessage for single messages, but
	// buildCombinedContent itself should still work correctly with one message.
	now := time.Now()
	msgs := []*discordgo.MessageCreate{
		msgAt("alice", "solo", now),
	}
	got := buildCombinedContent(msgs, "", "")
	if !strings.HasPrefix(got, "[1 messages arrived rapidly in quick succession]") {
		t.Errorf("unexpected output for single message: %q", got)
	}
	if !strings.Contains(got, "alice: solo") {
		t.Errorf("expected message line in output, got %q", got)
	}
	// No timestamp suffix for the only (first) message
	if strings.Contains(got, "(+") {
		t.Errorf("single message should not have timestamp suffix, got %q", got)
	}
}

func TestFormatMessageContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		botID   string
		botName string
		want    string
	}{
		{
			name:    "basic mention replacement",
			content: "hello <@123456> how are you",
			botID:   "123456",
			botName: "BotName",
			want:    "hello @BotName how are you",
		},
		{
			name:    "nickname mention replacement",
			content: "hello <@!123456> how are you",
			botID:   "123456",
			botName: "BotName",
			want:    "hello @BotName how are you",
		},
		{
			name:    "multiple mentions in one message",
			content: "<@123456> said hi and <@!123456> waved",
			botID:   "123456",
			botName: "BotName",
			want:    "@BotName said hi and @BotName waved",
		},
		{
			name:    "no mentions content unchanged",
			content: "just a regular message with no mentions",
			botID:   "123456",
			botName: "BotName",
			want:    "just a regular message with no mentions",
		},
		{
			name:    "empty botID and botName content unchanged",
			content: "hello there, no mentions at all",
			botID:   "",
			botName: "",
			want:    "hello there, no mentions at all",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMessageContent(tt.content, tt.botID, tt.botName)
			if got != tt.want {
				t.Errorf("formatMessageContent(%q, %q, %q) = %q, want %q",
					tt.content, tt.botID, tt.botName, got, tt.want)
			}
		})
	}
}

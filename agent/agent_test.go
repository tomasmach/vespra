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

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
	"github.com/tomasmach/vespra/tools"
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

func attachmentWithSize(contentType, url string, size int) *discordgo.MessageAttachment {
	return &discordgo.MessageAttachment{ContentType: contentType, URL: url, Size: size}
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

func TestHistoryUserContentReplyWithVideoOnly(t *testing.T) {
	m := &discordgo.Message{
		Author:  &discordgo.User{Username: "alice"},
		Content: "nice",
		ReferencedMessage: &discordgo.Message{
			Author:  &discordgo.User{Username: "bob"},
			Content: "",
			Attachments: []*discordgo.MessageAttachment{
				{ContentType: "video/mp4", URL: "https://cdn.example.com/clip.mp4"},
			},
		},
	}
	got := historyUserContent(m, "", "")
	want := `alice (replying to bob: "[video]"): nice`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestHistoryUserContentReplyWithImageAndVideo(t *testing.T) {
	m := &discordgo.Message{
		Author:  &discordgo.User{Username: "alice"},
		Content: "cool",
		ReferencedMessage: &discordgo.Message{
			Author:  &discordgo.User{Username: "bob"},
			Content: "",
			Attachments: []*discordgo.MessageAttachment{
				{ContentType: "image/png", URL: "https://cdn.example.com/img.png"},
				{ContentType: "video/mp4", URL: "https://cdn.example.com/clip.mp4"},
			},
		},
	}
	got := historyUserContent(m, "", "")
	want := `alice (replying to bob: "[image], [video]"): cool`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildUserMessageWithVideo(t *testing.T) {
	fakeData := []byte("fake video bytes")
	srv, cleanup := imageServer(t, fakeData)
	defer cleanup()

	a := attachment("video/mp4", srv.URL+"/clip.mp4")
	m := buildUserMessage(context.Background(), srv.Client(), msg("watch this", a), "", "")
	if len(m.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(m.ContentParts))
	}
	if m.ContentParts[0].Type != "text" {
		t.Errorf("expected first part type=text, got %q", m.ContentParts[0].Type)
	}
	if m.ContentParts[1].Type != "video_url" {
		t.Errorf("expected second part type=video_url, got %q", m.ContentParts[1].Type)
	}
	wantURL := fmt.Sprintf("data:video/mp4;base64,%s", base64.StdEncoding.EncodeToString(fakeData))
	if m.ContentParts[1].VideoURL.URL != wantURL {
		t.Errorf("unexpected video URL: %q", m.ContentParts[1].VideoURL.URL)
	}
}

func TestBuildUserMessageReferencedVideo(t *testing.T) {
	fakeData := []byte("fake ref video bytes")
	srv, cleanup := imageServer(t, fakeData)
	defer cleanup()

	refMsg := &discordgo.Message{
		Author:  &discordgo.User{Username: "bob"},
		Content: "",
		Attachments: []*discordgo.MessageAttachment{
			{ContentType: "video/mp4", URL: srv.URL + "/ref.mp4"},
		},
	}
	m := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content:           "check this out",
			Author:            &discordgo.User{Username: "alice"},
			ReferencedMessage: refMsg,
		},
	}
	result := buildUserMessage(context.Background(), srv.Client(), m, "", "")
	if len(result.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts (text + ref video), got %d", len(result.ContentParts))
	}
	if result.ContentParts[0].Type != "text" {
		t.Errorf("expected first part type=text, got %q", result.ContentParts[0].Type)
	}
	if result.ContentParts[1].Type != "video_url" {
		t.Errorf("expected second part type=video_url, got %q", result.ContentParts[1].Type)
	}
	wantURL := fmt.Sprintf("data:video/mp4;base64,%s", base64.StdEncoding.EncodeToString(fakeData))
	if result.ContentParts[1].VideoURL.URL != wantURL {
		t.Errorf("unexpected video URL: %q", result.ContentParts[1].VideoURL.URL)
	}
}

func TestBuildUserMessageVideoSkippedWhenTooLarge(t *testing.T) {
	a := attachmentWithSize("video/mp4", "https://cdn.example.com/huge.mp4", maxVideoBytes+1)
	m := buildUserMessage(context.Background(), &http.Client{}, msg("big video", a), "", "")
	// oversized video skipped → falls back to plain text
	if len(m.ContentParts) != 0 {
		t.Errorf("expected no content parts for oversized video, got %d", len(m.ContentParts))
	}
	if m.Content != "alice: big video" {
		t.Errorf("unexpected content: %q", m.Content)
	}
}

func TestHasGifEmbeds(t *testing.T) {
	gifv := &discordgo.Message{
		Embeds: []*discordgo.MessageEmbed{
			{Type: discordgo.EmbedTypeGifv, Thumbnail: &discordgo.MessageEmbedThumbnail{URL: "https://tenor.com/thumb.jpg"}},
		},
	}
	if !hasGifEmbeds(gifv) {
		t.Error("expected hasGifEmbeds=true for gifv embed with thumbnail")
	}

	noThumb := &discordgo.Message{
		Embeds: []*discordgo.MessageEmbed{
			{Type: discordgo.EmbedTypeGifv},
		},
	}
	if hasGifEmbeds(noThumb) {
		t.Error("expected hasGifEmbeds=false for gifv embed without thumbnail")
	}

	rich := &discordgo.Message{
		Embeds: []*discordgo.MessageEmbed{
			{Type: discordgo.EmbedTypeRich, Thumbnail: &discordgo.MessageEmbedThumbnail{URL: "https://example.com/img.jpg"}},
		},
	}
	if hasGifEmbeds(rich) {
		t.Error("expected hasGifEmbeds=false for non-gifv embed")
	}

	empty := &discordgo.Message{}
	if hasGifEmbeds(empty) {
		t.Error("expected hasGifEmbeds=false for message with no embeds")
	}
}

func TestHistoryUserContentReplyWithGif(t *testing.T) {
	m := &discordgo.Message{
		Author:  &discordgo.User{Username: "alice"},
		Content: "look",
		ReferencedMessage: &discordgo.Message{
			Author:  &discordgo.User{Username: "bob"},
			Content: "",
			Embeds: []*discordgo.MessageEmbed{
				{Type: discordgo.EmbedTypeGifv, Thumbnail: &discordgo.MessageEmbedThumbnail{URL: "https://tenor.com/thumb.jpg"}},
			},
		},
	}
	got := historyUserContent(m, "", "")
	want := `alice (replying to bob: "[gif]"): look`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildUserMessageWithGifEmbed(t *testing.T) {
	fakeData := []byte("fake jpeg thumbnail bytes")
	srv, cleanup := imageServer(t, fakeData)
	defer cleanup()

	gifMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content: "check this gif",
			Author:  &discordgo.User{Username: "alice"},
			Embeds: []*discordgo.MessageEmbed{
				{
					Type: discordgo.EmbedTypeGifv,
					Thumbnail: &discordgo.MessageEmbedThumbnail{
						ProxyURL: srv.URL + "/thumb.jpg",
						URL:      "https://tenor.com/original.gif",
					},
				},
			},
		},
	}
	m := buildUserMessage(context.Background(), srv.Client(), gifMsg, "", "")
	if len(m.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts (text + gif thumbnail), got %d", len(m.ContentParts))
	}
	if m.ContentParts[0].Type != "text" {
		t.Errorf("expected first part type=text, got %q", m.ContentParts[0].Type)
	}
	if m.ContentParts[1].Type != "image_url" {
		t.Errorf("expected second part type=image_url, got %q", m.ContentParts[1].Type)
	}
	if m.ContentParts[1].ImageURL == nil {
		t.Fatal("expected non-nil ImageURL")
	}
	if !strings.HasPrefix(m.ContentParts[1].ImageURL.URL, "data:") {
		t.Errorf("expected base64 data URL, got: %q", m.ContentParts[1].ImageURL.URL)
	}
}

func TestBuildUserMessageGifEmbedNoThumbnail(t *testing.T) {
	gifMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content: "a gif with no thumbnail",
			Author:  &discordgo.User{Username: "alice"},
			Embeds: []*discordgo.MessageEmbed{
				{Type: discordgo.EmbedTypeGifv}, // nil Thumbnail
			},
		},
	}
	m := buildUserMessage(context.Background(), nil, gifMsg, "", "")
	if len(m.ContentParts) != 0 {
		t.Errorf("expected no content parts for gifv embed with nil thumbnail, got %d", len(m.ContentParts))
	}
	if m.Content != "alice: a gif with no thumbnail" {
		t.Errorf("unexpected content: %q", m.Content)
	}
}

func TestBuildCombinedUserMessageWithGifEmbed(t *testing.T) {
	fakeData := []byte("fake jpeg thumbnail bytes")
	srv, cleanup := imageServer(t, fakeData)
	defer cleanup()

	gifMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content: "check this gif",
			Author:  &discordgo.User{Username: "alice"},
			Embeds: []*discordgo.MessageEmbed{
				{
					Type: discordgo.EmbedTypeGifv,
					Thumbnail: &discordgo.MessageEmbedThumbnail{
						ProxyURL: srv.URL + "/thumb.jpg",
						URL:      "https://tenor.com/original.gif",
					},
				},
			},
		},
	}
	plainMsg := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content: "just a regular message",
			Author:  &discordgo.User{Username: "bob"},
		},
	}

	a := &ChannelAgent{httpClient: srv.Client()}
	m := a.buildCombinedUserMessage(context.Background(), []*discordgo.MessageCreate{gifMsg, plainMsg}, "", "")

	if len(m.ContentParts) == 0 {
		t.Fatal("expected ContentParts to be non-empty")
	}
	if m.ContentParts[0].Type != "text" {
		t.Errorf("expected first part type=text, got %q", m.ContentParts[0].Type)
	}
	hasImageURL := false
	for _, part := range m.ContentParts {
		if part.Type == "image_url" {
			hasImageURL = true
			if part.ImageURL == nil {
				t.Fatal("expected non-nil ImageURL")
			}
			if !strings.HasPrefix(part.ImageURL.URL, "data:") {
				t.Errorf("expected base64 data URL, got: %q", part.ImageURL.URL)
			}
		}
	}
	if !hasImageURL {
		t.Error("expected an image_url part for the GIF thumbnail")
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

func TestSanitizeHistory(t *testing.T) {
	user := llm.Message{Role: "user", Content: "hello"}
	assistant := llm.Message{Role: "assistant", Content: "hi"}
	tool := llm.Message{Role: "tool", Content: "result"}

	tests := []struct {
		name string
		in   []llm.Message
		want []llm.Message
	}{
		{
			name: "empty input returns empty",
			in:   []llm.Message{},
			want: []llm.Message{},
		},
		{
			name: "already starts with user no change",
			in:   []llm.Message{user, assistant},
			want: []llm.Message{user, assistant},
		},
		{
			name: "starts with assistant drops it",
			in:   []llm.Message{assistant, user},
			want: []llm.Message{user},
		},
		{
			name: "starts with tool drops it",
			in:   []llm.Message{tool, user},
			want: []llm.Message{user},
		},
		{
			name: "multiple leading non-user messages all dropped",
			in:   []llm.Message{assistant, tool, assistant, user, assistant},
			want: []llm.Message{user, assistant},
		},
		{
			name: "all non-user messages returns empty",
			in:   []llm.Message{assistant, tool, assistant},
			want: []llm.Message{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeHistory(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("len(got)=%d, len(want)=%d; got=%v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i].Role != tt.want[i].Role || got[i].Content != tt.want[i].Content {
					t.Errorf("got[%d]=%+v, want[%d]=%+v", i, got[i], i, tt.want[i])
				}
			}
		})
	}
}

func TestShouldSuppressSmartMode(t *testing.T) {
	tests := []struct {
		name            string
		mode            string
		hasContent      bool
		replied         bool
		reacted         bool
		addressed       bool
		internal        bool
		webSearch       bool
		imageGen        bool
		directedAtOther bool
		want            bool
	}{
		{
			name:       "normal suppression: smart, non-addressed, no flags",
			mode:       "smart",
			hasContent: true,
			want:       true,
		},
		{
			name:       "internal messages not suppressed",
			mode:       "smart",
			hasContent: true,
			internal:   true,
			want:       false,
		},
		{
			name:       "web search not suppressed",
			mode:       "smart",
			hasContent: true,
			webSearch:  true,
			want:       false,
		},
		{
			name:       "image gen not suppressed",
			mode:       "smart",
			hasContent: true,
			imageGen:   true,
			want:       false,
		},
		{
			name:       "addressed not suppressed",
			mode:       "smart",
			hasContent: true,
			addressed:  true,
			want:       false,
		},
		{
			// Vision responses no longer bypass suppression — the vision model
			// is a pre-processing step, not the main chat model.
			name:       "image message suppressed in smart mode like any other",
			mode:       "smart",
			hasContent: true,
			want:       true,
		},
		{
			name:       "non-smart mode not suppressed",
			mode:       "always",
			hasContent: true,
			want:       false,
		},
		{
			name:       "replied not suppressed",
			mode:       "smart",
			hasContent: true,
			replied:    true,
			want:       false,
		},
		{
			// Reacted=true means the bot only reacted (emoji) and did not reply.
			// A react-only turn still suppresses smart mode — there is no text
			// response to deliver, so suppression is correct behaviour.
			name:       "reacted does not prevent suppression",
			mode:       "smart",
			hasContent: true,
			reacted:    true,
			want:       true,
		},
		{
			name: "no content not suppressed",
			mode: "smart",
			want: false,
		},
		{
			name:            "directed at other suppresses even with web search",
			mode:            "smart",
			hasContent:      true,
			webSearch:       true,
			directedAtOther: true,
			want:            true,
		},
		{
			name:            "directed at other suppresses even with image gen",
			mode:            "smart",
			hasContent:      true,
			imageGen:        true,
			directedAtOther: true,
			want:            true,
		},
		{
			name:            "directed at other does not override addressed",
			mode:            "smart",
			hasContent:      true,
			addressed:       true,
			directedAtOther: true,
			want:            false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := tools.NewRegistry()
			reg.Replied = tt.replied
			reg.Reacted = tt.reacted
			reg.WebSearchCalled = tt.webSearch
			reg.ImageGenCalled = tt.imageGen
			got := shouldSuppressSmartMode(tt.mode, tt.hasContent, reg, tt.addressed, tt.internal, tt.directedAtOther)
			if got != tt.want {
				t.Errorf("shouldSuppressSmartMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSendAllowed(t *testing.T) {
	cfgStore := config.NewStoreFromConfig(&config.Config{
		Agent: config.TurnConfig{
			SendRateLimit:         3,
			SendRateWindowSeconds: 60,
		},
	})
	a := &ChannelAgent{cfgStore: cfgStore}

	// First 3 sends are allowed.
	for i := 0; i < 3; i++ {
		if !a.sendAllowed() {
			t.Fatalf("send %d should be allowed", i+1)
		}
	}

	// 4th send exceeds the limit.
	if a.sendAllowed() {
		t.Error("4th send should be rate-limited")
	}
}

func TestSendAllowedWindowExpiry(t *testing.T) {
	cfgStore := config.NewStoreFromConfig(&config.Config{
		Agent: config.TurnConfig{
			SendRateLimit:         2,
			SendRateWindowSeconds: 1,
		},
	})
	a := &ChannelAgent{cfgStore: cfgStore}

	// Fill the window.
	a.sendAllowed()
	a.sendAllowed()
	if a.sendAllowed() {
		t.Fatal("should be rate-limited after 2 sends")
	}

	// Backdate timestamps to simulate window expiry.
	for i := range a.sendTimestamps {
		a.sendTimestamps[i] = a.sendTimestamps[i].Add(-2 * time.Second)
	}

	if !a.sendAllowed() {
		t.Error("send should be allowed after window expires")
	}
}

func TestBuildSystemPromptSmartAddressed(t *testing.T) {
	a := &ChannelAgent{soulText: "You are a test bot."}
	cfg := &config.Config{}
	got := a.buildSystemPrompt(cfg, "smart", "test-chan", nil, "TestBot", true, false)
	if !strings.Contains(got, "MUST respond") {
		t.Errorf("expected smart+addressed prompt to contain 'MUST respond', got:\n%s", got)
	}
}

func TestBuildSystemPromptSmartNotAddressed(t *testing.T) {
	a := &ChannelAgent{soulText: "You are a test bot."}
	cfg := &config.Config{}
	got := a.buildSystemPrompt(cfg, "smart", "test-chan", nil, "TestBot", false, false)
	if !strings.Contains(got, "Decide whether to respond") {
		t.Errorf("expected smart+not-addressed prompt to contain 'Decide whether to respond', got:\n%s", got)
	}
}

func TestBuildSystemPromptNonSmart(t *testing.T) {
	a := &ChannelAgent{soulText: "You are a test bot."}
	cfg := &config.Config{}
	got := a.buildSystemPrompt(cfg, "always", "test-chan", nil, "TestBot", false, false)
	if strings.Contains(got, "smart mode") {
		t.Errorf("non-smart prompt should not contain 'smart mode', got:\n%s", got)
	}
}

func TestBuildSystemPromptSmartDirectedAtOther(t *testing.T) {
	a := &ChannelAgent{soulText: "You are a test bot."}
	cfg := &config.Config{}
	got := a.buildSystemPrompt(cfg, "smart", "test-chan", nil, "TestBot", false, true)
	if !strings.Contains(got, "MUST stay silent") {
		t.Errorf("expected directed-at-other prompt to contain 'MUST stay silent', got:\n%s", got)
	}
	if !strings.Contains(got, "directed at another") {
		t.Errorf("expected directed-at-other prompt to contain 'directed at another', got:\n%s", got)
	}
}

func TestBuildSystemPromptSmartAddressedOverridesDirectedAtOther(t *testing.T) {
	a := &ChannelAgent{soulText: "You are a test bot."}
	cfg := &config.Config{}
	got := a.buildSystemPrompt(cfg, "smart", "test-chan", nil, "TestBot", true, true)
	if !strings.Contains(got, "MUST respond") {
		t.Errorf("addressed should override directedAtOther, expected 'MUST respond', got:\n%s", got)
	}
	if strings.Contains(got, "MUST stay silent") {
		t.Errorf("addressed should override directedAtOther, should not contain 'MUST stay silent', got:\n%s", got)
	}
}

func TestIsDirectedAtOther(t *testing.T) {
	const botID = "bot123"
	const botName = "Machmonstrum"

	tests := []struct {
		name string
		msg  *discordgo.MessageCreate
		want bool
	}{
		{
			name: "Discord mention of other user only",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID:  "g1",
				Content:  "<@999> dáme fotbálek?",
				Mentions: []*discordgo.User{{ID: "999"}},
			}},
			want: true,
		},
		{
			name: "Discord mention of bot only",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID:  "g1",
				Content:  "<@bot123> help",
				Mentions: []*discordgo.User{{ID: botID}},
			}},
			want: false,
		},
		{
			name: "both bot and other mentioned",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID:  "g1",
				Content:  "<@bot123> <@999> what?",
				Mentions: []*discordgo.User{{ID: botID}, {ID: "999"}},
			}},
			want: false,
		},
		{
			name: "plain text @Name, at start",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@Petře, dáme fotbálek?",
			}},
			want: true,
		},
		{
			name: "plain text @Name: at start",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@Petr: kdy máš čas?",
			}},
			want: true,
		},
		{
			name: "plain text @Name space at start",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@Petr what time?",
			}},
			want: true,
		},
		{
			name: "plain text @BotName vocative form",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@Machmonstře, řekni vtip",
			}},
			want: false,
		},
		{
			name: "plain text @BotName exact match",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@Machmonstrum řekni vtip",
			}},
			want: false,
		},
		{
			name: "plain text @BotName case insensitive",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@machmonstrum hello",
			}},
			want: false,
		},
		{
			name: "no mentions general question",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "Ví někdo jaký je počasí?",
			}},
			want: false,
		},
		{
			name: "no mentions statement",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "Dal jsem si guláš",
			}},
			want: false,
		},
		{
			name: "DM with other mention",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID:  "",
				Content:  "<@999> hey",
				Mentions: []*discordgo.User{{ID: "999"}},
			}},
			want: false,
		},
		{
			name: "lone @ with space",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@ something",
			}},
			want: false,
		},
		{
			name: "email mid-message",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "send to user@example.com",
			}},
			want: false,
		},
		{
			name: "@everyone",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@everyone check this",
			}},
			want: false,
		},
		{
			name: "@here",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@here meeting now",
			}},
			want: false,
		},
		{
			name: "mid-message @name",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "Hey @Petr, co říkáš?",
			}},
			want: false,
		},
		{
			// plainTextMentionRe requires a trailing [\s,:] so a bare @Name at
			// end-of-message (no trailing character) does NOT match.
			name: "bare @Name at end of message",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "@Petr",
			}},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isDirectedAtOther(tt.msg, botID, botName)
			if got != tt.want {
				t.Errorf("isDirectedAtOther() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLooksLikeBotName(t *testing.T) {
	tests := []struct {
		name, botName string
		want          bool
	}{
		{"machmonstře", "machmonstrum", true},   // vocative (10/12 = 83%)
		{"machmonstrum", "machmonstrum", true},  // exact
		{"machmonstrume", "machmonstrum", true}, // another declension (12/13 = 92%)
		{"petr", "machmonstrum", false},         // completely different
		{"mac", "machmonstrum", false},          // too short (shared 3 < min 4)
		{"ma", "machmonstrum", false},           // too short
		{"machmon", "machmonstrum", false},      // only 58% of longer
		{"vespro", "vespra", true},              // vocative feminine (5/6 = 83%)
		{"vespra", "vespra", true},              // exact
		{"botname", "botname", true},            // exact short name
		{"botnam", "botname", true},             // 1 rune off (6/7 = 86%)
		{"botn", "botname", false},              // only 57% of longer
		{"ve\u0301spra", "véspra", true},        // NFD decomposed é vs NFC precomposed é — exact match after normalization
	}
	for _, tt := range tests {
		t.Run(tt.name+"_vs_"+tt.botName, func(t *testing.T) {
			got := looksLikeBotName(tt.name, tt.botName)
			if got != tt.want {
				t.Errorf("looksLikeBotName(%q, %q) = %v, want %v", tt.name, tt.botName, got, tt.want)
			}
		})
	}
}

func TestContainsBotName(t *testing.T) {
	tests := []struct {
		content, botName string
		want             bool
	}{
		{"Machmonstře, řekni vtip", "Machmonstrum", true},
		{"Hey Machmonstrum!", "Machmonstrum", true},
		{"@Machmonstře hello", "Machmonstrum", true},
		{"petr dáme fotbálek", "Machmonstrum", false},
		{"mac co je?", "Machmonstrum", false},
		{"", "Machmonstrum", false},
		{"hello", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.content+"_"+tt.botName, func(t *testing.T) {
			got := containsBotName(tt.content, tt.botName)
			if got != tt.want {
				t.Errorf("containsBotName(%q, %q) = %v, want %v", tt.content, tt.botName, got, tt.want)
			}
		})
	}
}

func TestIsAddressedToBotNameMention(t *testing.T) {
	const botID = "bot123"
	const botName = "Machmonstrum"

	tests := []struct {
		name string
		msg  *discordgo.MessageCreate
		want bool
	}{
		{
			name: "plain-text vocative in guild",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "Machmonstře, řekni vtip",
			}},
			want: true,
		},
		{
			name: "exact name in guild",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "Hey Machmonstrum!",
			}},
			want: true,
		},
		{
			name: "unrelated message in guild",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "petr dáme fotbálek",
			}},
			want: false,
		},
		{
			name: "DM always true",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "",
				Content: "random message",
			}},
			want: true,
		},
		{
			name: "Discord @mention still works",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "<@bot123> help",
			}},
			want: true,
		},
		{
			name: "reply to bot in guild",
			msg: &discordgo.MessageCreate{Message: &discordgo.Message{
				GuildID: "g1",
				Content: "yes exactly",
				MessageReference: &discordgo.MessageReference{MessageID: "ref1"},
				ReferencedMessage: &discordgo.Message{
					Author: &discordgo.User{ID: botID},
				},
			}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAddressedToBot(tt.msg, botID, botName)
			if got != tt.want {
				t.Errorf("isAddressedToBot() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldSendFallback(t *testing.T) {
	tests := []struct {
		name       string
		internal   bool
		replied    bool
		imageGen   bool
		webSearch  bool
		reacted    bool
		hasContent bool
		addressed  bool
		want       bool
	}{
		{
			name:      "addressed with no response sends fallback",
			addressed: true,
			want:      true,
		},
		{
			name:      "react-only addressed turn does NOT send fallback",
			addressed: true,
			reacted:   true,
			want:      false,
		},
		{
			name:      "replied does not send fallback",
			addressed: true,
			replied:   true,
			want:      false,
		},
		{
			name:      "image gen does not send fallback",
			addressed: true,
			imageGen:  true,
			want:      false,
		},
		{
			name:      "web search does not send fallback",
			addressed: true,
			webSearch: true,
			want:      false,
		},
		{
			name:       "has content does not send fallback",
			addressed:  true,
			hasContent: true,
			want:       false,
		},
		{
			name:     "internal does not send fallback",
			internal: true,
			want:     false,
		},
		{
			name: "non-addressed does not send fallback",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := tools.NewRegistry()
			reg.Replied = tt.replied
			reg.ImageGenCalled = tt.imageGen
			reg.WebSearchCalled = tt.webSearch
			reg.Reacted = tt.reacted
			got := shouldSendFallback(tt.internal, reg, tt.hasContent, tt.addressed)
			if got != tt.want {
				t.Errorf("shouldSendFallback() = %v, want %v", got, tt.want)
			}
		})
	}
}


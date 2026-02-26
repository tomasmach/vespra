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

	"github.com/tomasmach/vespra/llm"
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

func guildMsg(guildID, content string, ref *discordgo.Message) *discordgo.MessageCreate {
	m := &discordgo.MessageCreate{
		Message: &discordgo.Message{
			GuildID: guildID,
			Content: content,
			Author:  &discordgo.User{Username: "alice"},
		},
	}
	if ref != nil {
		m.MessageReference = &discordgo.MessageReference{MessageID: "ref"}
		m.ReferencedMessage = ref
	}
	return m
}

func TestIsAddressedToBot(t *testing.T) {
	botID := "123"
	botName := "Vespra"

	botAuthor := &discordgo.User{ID: botID, Username: "Vespra"}

	tests := []struct {
		name    string
		m       *discordgo.MessageCreate
		botName string
		want    bool
	}{
		{
			name:    "DM always addressed",
			m:       guildMsg("", "just chatting", nil),
			botName: botName,
			want:    true,
		},
		{
			name: "mention via <@botID>",
			m: &discordgo.MessageCreate{
				Message: &discordgo.Message{
					GuildID: "guild1",
					Content: "hello <@123> how are you",
					Author:  &discordgo.User{Username: "alice"},
				},
			},
			botName: botName,
			want:    true,
		},
		{
			name: "mention via <@!botID>",
			m: &discordgo.MessageCreate{
				Message: &discordgo.Message{
					GuildID: "guild1",
					Content: "hello <@!123> how are you",
					Author:  &discordgo.User{Username: "alice"},
				},
			},
			botName: botName,
			want:    true,
		},
		{
			name:    "reply to bot message",
			m:       guildMsg("guild1", "yes I agree", &discordgo.Message{Author: botAuthor}),
			botName: botName,
			want:    true,
		},
		{
			name:    "name exact match",
			m:       guildMsg("guild1", "Vespra can you help me?", nil),
			botName: botName,
			want:    true,
		},
		{
			name:    "name lowercase",
			m:       guildMsg("guild1", "hey vespra, what time is it", nil),
			botName: botName,
			want:    true,
		},
		{
			name:    "name uppercase",
			m:       guildMsg("guild1", "VESPRA please help", nil),
			botName: botName,
			want:    true,
		},
		{
			name:    "name mixed case",
			m:       guildMsg("guild1", "VeSpRa are you there?", nil),
			botName: botName,
			want:    true,
		},
		{
			name:    "unrelated message",
			m:       guildMsg("guild1", "just chatting here", nil),
			botName: botName,
			want:    false,
		},
		{
			name:    "empty botName with matching text",
			m:       guildMsg("guild1", "vespra hello", nil),
			botName: "",
			want:    false,
		},
		{
			name:    "guild message not addressed baseline",
			m:       guildMsg("guild1", "random stuff", nil),
			botName: botName,
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAddressedToBot(tt.m, botID, tt.botName)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBotRecentlySpoke(t *testing.T) {
	user := llm.Message{Role: "user", Content: "hello"}
	assistant := llm.Message{Role: "assistant", Content: "hi"}
	tool := llm.Message{Role: "tool", Content: "result"}

	tests := []struct {
		name    string
		history []llm.Message
		want    bool
	}{
		{"empty history", []llm.Message{}, false},
		{"last is user", []llm.Message{assistant, user}, false},
		{"last is assistant", []llm.Message{user, assistant}, true},
		{"only assistant", []llm.Message{assistant}, true},
		{"last is tool", []llm.Message{user, assistant, tool}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := botRecentlySpoke(tt.history)
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
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

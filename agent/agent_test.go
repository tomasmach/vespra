package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	m := buildUserMessage(context.Background(), nil, msg("hello"))
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
	m := buildUserMessage(context.Background(), srv.Client(), msg("look", a))
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
	m := buildUserMessage(context.Background(), nil, msg("file", attachment("application/pdf", "https://cdn.example.com/doc.pdf")))
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
	))
	if len(m.ContentParts) != 3 { // text + 2 images
		t.Fatalf("expected 3 content parts (text + 2 images), got %d", len(m.ContentParts))
	}
}

func TestBuildUserMessageImageDownloadFails(t *testing.T) {
	// Use an unreachable URL to simulate download failure
	a := attachment("image/png", "http://127.0.0.1:1") // nothing listening
	m := buildUserMessage(context.Background(), &http.Client{}, msg("look", a))
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

	m := buildUserMessage(context.Background(), srv.Client(), msg("", attachment("image/gif", srv.URL+"/anim.gif")))
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
	got := historyUserContent(m)
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
			Author: &discordgo.User{Username: "bob"},
		},
	}
	got := historyUserContent(m)
	want := "alice (replying to bob): I agree"
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
	got := historyUserContent(m)
	want := "alice: hello"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

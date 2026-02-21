package agent

import (
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

func TestBuildUserMessageTextOnly(t *testing.T) {
	m := buildUserMessage(msg("hello"))
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
	m := buildUserMessage(msg("look", attachment("image/png", "https://cdn.example.com/img.png")))
	if len(m.ContentParts) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(m.ContentParts))
	}
	if m.ContentParts[0].Type != "text" {
		t.Errorf("expected first part type=text, got %q", m.ContentParts[0].Type)
	}
	if m.ContentParts[1].Type != "image_url" {
		t.Errorf("expected second part type=image_url, got %q", m.ContentParts[1].Type)
	}
	if m.ContentParts[1].ImageURL.URL != "https://cdn.example.com/img.png" {
		t.Errorf("unexpected image URL: %q", m.ContentParts[1].ImageURL.URL)
	}
}

func TestBuildUserMessageNonImageAttachmentIgnored(t *testing.T) {
	m := buildUserMessage(msg("file", attachment("application/pdf", "https://cdn.example.com/doc.pdf")))
	if len(m.ContentParts) != 0 {
		t.Errorf("expected no content parts for non-image, got %d", len(m.ContentParts))
	}
	if m.Content != "alice: file" {
		t.Errorf("unexpected content: %q", m.Content)
	}
}

func TestBuildUserMessageMixedAttachments(t *testing.T) {
	m := buildUserMessage(msg("mixed",
		attachment("image/jpeg", "https://cdn.example.com/photo.jpg"),
		attachment("application/pdf", "https://cdn.example.com/doc.pdf"),
		attachment("image/webp", "https://cdn.example.com/pic.webp"),
	))
	if len(m.ContentParts) != 3 { // text + 2 images
		t.Fatalf("expected 3 content parts (text + 2 images), got %d", len(m.ContentParts))
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

func TestBuildUserMessageEmptyText(t *testing.T) {
	// image with no text caption
	m := buildUserMessage(msg("", attachment("image/gif", "https://cdn.example.com/anim.gif")))
	if len(m.ContentParts) != 2 {
		t.Fatalf("expected 2 parts, got %d", len(m.ContentParts))
	}
	// text part should still carry the "alice: " prefix even with empty content
	if m.ContentParts[0].Text != "alice: " {
		t.Errorf("unexpected text part: %q", m.ContentParts[0].Text)
	}
}

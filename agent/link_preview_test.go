package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestBotMessagesSuppressLinkPreviewsButKeepImages(t *testing.T) {
	var mu sync.Mutex
	var flags []discordgo.MessageFlags
	var files []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := []byte{}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			payload = []byte(r.FormValue("payload_json"))
			for _, fh := range r.MultipartForm.File {
				mu.Lock()
				files = append(files, fh[0].Filename)
				mu.Unlock()
			}
		} else {
			payload, _ = io.ReadAll(r.Body)
		}
		var sent struct {
			Flags discordgo.MessageFlags `json:"flags"`
		}
		if err := json.Unmarshal(payload, &sent); err != nil {
			t.Error(err)
		}
		mu.Lock()
		flags = append(flags, sent.Flags)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"sent"}`)
	}))
	defer srv.Close()
	session, err := discordgo.New("Bot fake")
	if err != nil {
		t.Fatal(err)
	}
	session.Client = &http.Client{Transport: scheduledTaskTransport{baseURL: srv.URL}, Timeout: 2 * time.Second}
	a := &ChannelAgent{resources: &AgentResources{Session: session}}

	if err := a.sendText("channel1", "see https://example.com"); err != nil {
		t.Fatal(err)
	}
	if err := a.makeSendImageFn("channel1")("cat.png", strings.NewReader("png"), "made at https://example.com"); err != nil {
		t.Fatal(err)
	}

	for i, f := range flags {
		if f&discordgo.MessageFlagsSuppressEmbeds == 0 {
			t.Errorf("message %d: flags = %d, want link previews suppressed", i, f)
		}
	}
	if len(flags) != 2 || !slices.Equal(files, []string{"cat.png"}) {
		t.Errorf("sent %d messages with files %v, want 2 messages and the image attached", len(flags), files)
	}
}

package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/vespra/tools"
)

// redirectTransport sends Discord API requests to a test server.
type redirectTransport struct {
	target *url.URL
}

func (t redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	redirected := req.Clone(req.Context())
	redirected.URL.Scheme, redirected.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(redirected)
}

func fakeDiscord(t *testing.T, mentions []*discordgo.User, handler http.HandlerFunc) *tools.Registry {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	session, err := discordgo.New("Bot fake")
	if err != nil {
		t.Fatal(err)
	}
	session.Client = &http.Client{Transport: redirectTransport{target: target}, Timeout: 2 * time.Second}
	reg := tools.NewRegistry()
	tools.RegisterMessageSearch(reg, session, "guild1", "channel1", mentions)
	return reg
}

func isMemberSearch(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/guilds/guild1/members/search")
}

func TestSearchMessagesSendsFiltersAndFormatsResults(t *testing.T) {
	var mu sync.Mutex
	var searched url.Values
	sent := time.Date(2026, 10, 20, 14, 3, 0, 0, time.Local).Format(time.RFC3339)
	pajaMention := &discordgo.User{ID: "222", Username: "paulee5", GlobalName: "Pája"}
	reg := fakeDiscord(t, []*discordgo.User{pajaMention}, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isMemberSearch(r) && r.URL.Query().Get("query") == "roy":
			fmt.Fprint(w, `[{"user":{"id":"111","username":"roytak"}}]`)
		case strings.HasSuffix(r.URL.Path, "/guilds/guild1/messages/search"):
			mu.Lock()
			searched = r.URL.Query()
			mu.Unlock()
			fmt.Fprintf(w, `{"total_results":30,"messages":[[{"id":"m1","channel_id":"channel1","timestamp":%q,"content":"kdo jde na pivo <@111>","author":{"id":"222","username":"paulee5"},"mentions":[{"id":"111","username":"roytak"}],"attachments":[{"id":"a1","filename":"pivo.jpg"}]}],[{"id":"m2","channel_id":"channel1","timestamp":%q,"content":"já","author":{"id":"111","username":"roytak"}}]]}`, sent, sent)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	// The model knows Pája only from the mention, shown by display name.
	result, err := reg.Dispatch(context.Background(), "search_messages", json.RawMessage(`{"content":"pivo","author":"Pája","mentions":"@roy","after":"2026-10-20","before":"2026-10-21 18:30","has":"image","oldest_first":true}`))
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	for param, want := range map[string]string{"channel_id": "channel1", "include_nsfw": "true", "content": "pivo", "author_id": "222", "mentions": "111", "has": "image", "sort_order": "asc", "offset": ""} {
		if got := searched.Get(param); got != want {
			t.Errorf("%s = %q, want %q", param, got, want)
		}
	}
	// min_id and max_id are exclusive, so min_id sits just before midnight.
	for param, want := range map[string]time.Time{
		"min_id": time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local).Add(-time.Millisecond),
		"max_id": time.Date(2026, 10, 21, 18, 30, 0, 0, time.Local),
	} {
		got, err := discordgo.SnowflakeTimestamp(searched.Get(param))
		if err != nil || !got.Equal(want) {
			t.Errorf("%s is at %v (%v), want %v", param, got, err, want)
		}
	}
	for _, want := range []string{
		"30 messages match. Showing 1-2; call again with offset 25 for more.\n",
		"[2026-10-20 14:03] paulee5 (<@222>): kdo jde na pivo @roytak (<@111>) [attachment pivo.jpg] https://discord.com/channels/guild1/channel1/m1\n",
		"[2026-10-20 14:03] roytak (<@111>): já https://discord.com/channels/guild1/channel1/m2\n",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("result %q lacks %q", result, want)
		}
	}
}

func TestSearchMessagesResolvesPerson(t *testing.T) {
	for _, tc := range []struct {
		name    string
		person  string
		members string
		wantID  string
		wantErr string
	}{
		{"username beats nickname", "tom", `[{"nick":"tom","user":{"id":"2","username":"tomas"}},{"user":{"id":"1","username":"tom"}}]`, "1", ""},
		{"unique nickname", "Pája", `[{"user":{"id":"3","username":"pajik"}},{"nick":"Pája","user":{"id":"2","username":"paulee5"}}]`, "2", ""},
		{"single prefix match", "roy", `[{"user":{"id":"4","username":"roytak"}}]`, "4", ""},
		{"numeric nickname", "123", `[{"nick":"123","user":{"id":"5","username":"numbers"}}]`, "5", ""},
		{"user ID", "123456789012345678", "", "123456789012345678", ""},
		{"shared nickname", "tom", `[{"nick":"tom","user":{"id":"1","username":"tomas"}},{"nick":"tom","user":{"id":"2","username":"tommy"}}]`, "", "(tomas, tommy)"},
		{"several prefix matches", "to", `[{"user":{"id":"1","username":"tomas"}},{"user":{"id":"2","username":"tommy"}}]`, "", "(tomas, tommy)"},
		{"nobody", "tom", `[]`, "", "no member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var authorID string
			reg := fakeDiscord(t, nil, func(w http.ResponseWriter, r *http.Request) {
				if isMemberSearch(r) {
					if tc.members == "" {
						t.Error("looked up a user ID as a name")
					}
					fmt.Fprint(w, tc.members)
					return
				}
				mu.Lock()
				authorID = r.URL.Query().Get("author_id")
				mu.Unlock()
				fmt.Fprint(w, `{"total_results":0,"messages":[]}`)
			})

			_, err := reg.Dispatch(context.Background(), "search_messages", json.RawMessage(fmt.Sprintf(`{"author":%q}`, tc.person)))

			mu.Lock()
			defer mu.Unlock()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || authorID != "" {
					t.Errorf("error %v and search by %q, want error %q and no search", err, authorID, tc.wantErr)
				}
				return
			}
			if err != nil || authorID != tc.wantID {
				t.Errorf("searched author %q (%v), want %q", authorID, err, tc.wantID)
			}
		})
	}
}

func TestSearchMessagesRejectsSharedDisplayNameInMentions(t *testing.T) {
	mentions := []*discordgo.User{
		{ID: "111", Username: "pavla", GlobalName: "Pája"},
		{ID: "111", Username: "pavla", GlobalName: "Pája"},
		{ID: "222", Username: "paulee5", GlobalName: "Pája"},
	}
	reg := fakeDiscord(t, mentions, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
	})

	_, err := reg.Dispatch(context.Background(), "search_messages", json.RawMessage(`{"author":"Pája"}`))
	if err == nil || !strings.Contains(err.Error(), "several mentioned people") {
		t.Errorf("error %v, want several mentioned people", err)
	}
	// The same person mentioned twice, as in a batch of messages, is not ambiguous.
	reg = fakeDiscord(t, mentions[:2], func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("author_id"); got != "111" {
			t.Errorf("author_id = %q, want 111", got)
		}
		fmt.Fprint(w, `{"total_results":0,"messages":[]}`)
	})
	if _, err := reg.Dispatch(context.Background(), "search_messages", json.RawMessage(`{"author":"Pája"}`)); err != nil {
		t.Errorf("repeated mention: %v", err)
	}
}

func TestSearchMessagesStopsPagingAtDiscordLimit(t *testing.T) {
	reg := fakeDiscord(t, nil, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"total_results":10001,"messages":[[{"id":"m1","channel_id":"channel1","timestamp":"2026-10-20T14:03:00Z","content":"old","author":{"id":"1","username":"roytak"}}]]}`)
	})

	result, err := reg.Dispatch(context.Background(), "search_messages", json.RawMessage(`{"offset":9975}`))

	if err != nil || !strings.HasPrefix(result, "10001 messages match. Showing 9976-9976; Discord pages no further, so narrow the time range for more.\n") {
		t.Errorf("result %q, %v", result, err)
	}
}

func TestSearchMessagesWaitsForIndex(t *testing.T) {
	for _, tc := range []struct {
		name         string
		retryAfter   float64
		wantSearches int
		wantResult   bool
	}{
		{"short wait", 0.01, 2, true},
		{"long wait", 20, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			searches := 0
			reg := fakeDiscord(t, nil, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				searches++
				first := searches == 1
				mu.Unlock()
				if first {
					w.WriteHeader(http.StatusAccepted)
					fmt.Fprintf(w, `{"message":"Index not yet available. Try again later","code":110000,"documents_indexed":0,"retry_after":%s}`, strconv.FormatFloat(tc.retryAfter, 'f', -1, 64))
					return
				}
				fmt.Fprint(w, `{"total_results":1,"messages":[[{"id":"m1","channel_id":"channel1","timestamp":"2026-10-20T14:03:00Z","content":"indexed","author":{"id":"1","username":"roytak"}}]]}`)
			})

			result, err := reg.Dispatch(context.Background(), "search_messages", json.RawMessage(`{"content":"indexed"}`))

			mu.Lock()
			defer mu.Unlock()
			if searches != tc.wantSearches {
				t.Errorf("searched %d times, want %d", searches, tc.wantSearches)
			}
			if tc.wantResult && (err != nil || !strings.Contains(result, "roytak (<@1>): indexed")) {
				t.Errorf("result %q, %v", result, err)
			}
			if !tc.wantResult && (err == nil || !strings.Contains(err.Error(), "Index not yet available")) {
				t.Errorf("error %v, want Discord's index message", err)
			}
		})
	}
}

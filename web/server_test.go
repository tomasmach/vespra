package web_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tomasmach/vespra/agent"
	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/web"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	return newTestServerWithAgents(t, "")
}

// newTestServerWithAgents creates a test server with pre-seeded agents written
// directly to the config TOML, bypassing handleCreateAgent validation.
func newTestServerWithAgents(t *testing.T, agentsTOML string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	base := "[bot]\ntoken=\"x\"\n[llm]\nopenrouter_key=\"test\"\n"
	if err := os.WriteFile(cfgPath, []byte(base+agentsTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := web.New(":0", store, cfgPath, &agent.Router{}, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, dir
}

func TestAgentSoulDirTraversal(t *testing.T) {
	// Inject an agent whose ID is "../evil" directly in TOML, bypassing
	// handleCreateAgent's HTTP-layer validation.
	agentsTOML := "\n[[agents]]\nid = \"../evil\"\nserver_id = \"999\"\n"
	ts, _ := newTestServerWithAgents(t, agentsTOML)

	// GET /api/agents/..%2Fevil/souls — agentSoulDir must return "" and the
	// handler must respond with 400 rather than listing or creating files.
	resp, err := http.Get(ts.URL + "/api/agents/..%2Fevil/souls")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal id: expected 400, got %d", resp.StatusCode)
	}
}

func TestAgentSoulDirUnicode(t *testing.T) {
	ts, _ := newTestServer(t)

	// Create an agent with a Unicode + space ID via the HTTP API.
	// handleCreateAgent now accepts Unicode IDs up to 128 runes.
	body := `{"id":"Čeština agent","server_id":"777888999"}`
	resp, err := http.Post(ts.URL+"/api/agents", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unicode agent: expected 201, got %d", resp.StatusCode)
	}

	// GET /api/agents/<percent-encoded id>/souls — agentSoulDir must return a
	// non-empty path, so the handler responds with 200.
	resp, err = http.Get(ts.URL + "/api/agents/" + url.PathEscape("Čeština agent") + "/souls")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unicode agent souls list: expected 200, got %d", resp.StatusCode)
	}
}

func TestHandleCreateAgentIDValidation(t *testing.T) {
	// 129-rune ID (one over the 128-rune limit).
	longID := strings.Repeat("x", 129)
	// Exactly 128 runes — must be accepted.
	maxID := strings.Repeat("x", 128)

	tests := []struct {
		name     string
		id       string
		serverID string
		want     int
	}{
		{"dot rejected", ".", "100000001", http.StatusBadRequest},
		{"dotdot rejected", "..", "100000002", http.StatusBadRequest},
		{"slash in id rejected", "foo/bar", "100000003", http.StatusBadRequest},
		{"unicode id accepted", "Čeština agent", "100000004", http.StatusCreated},
		{"exactly 128 runes accepted", maxID, "100000005", http.StatusCreated},
		{"129 runes rejected", longID, "100000006", http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := newTestServer(t)
			body := `{"id":` + jsonString(tc.id) + `,"server_id":"` + tc.serverID + `"}`
			resp, err := http.Post(ts.URL+"/api/agents", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("id=%q: expected %d, got %d", tc.id, tc.want, resp.StatusCode)
			}
		})
	}
}

// jsonString encodes s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestAgentSoulLibrary(t *testing.T) {
	ts, _ := newTestServer(t)

	// Create an agent first.
	agentBody := `{"id":"test-agent","server_id":"111222333"}`
	resp, err := http.Post(ts.URL+"/api/agents", "application/json", strings.NewReader(agentBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create agent: got %d", resp.StatusCode)
	}

	// List souls — should be empty.
	resp, err = http.Get(ts.URL + "/api/agents/test-agent/souls")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list souls: got %d: %s", resp.StatusCode, data)
	}
	var listResp map[string]any
	if err := json.Unmarshal(data, &listResp); err != nil {
		t.Fatal(err)
	}
	souls := listResp["souls"].([]any)
	if len(souls) != 0 {
		t.Fatalf("expected 0 souls, got %d", len(souls))
	}

	// Create a soul.
	resp, err = http.Post(ts.URL+"/api/agents/test-agent/souls", "application/json",
		strings.NewReader(`{"name":"friendly","content":"You are friendly."}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create soul: got %d", resp.StatusCode)
	}

	// Duplicate create should conflict.
	resp, err = http.Post(ts.URL+"/api/agents/test-agent/souls", "application/json",
		strings.NewReader(`{"name":"friendly","content":"duplicate"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate soul: expected 409, got %d", resp.StatusCode)
	}

	// Invalid name should be rejected.
	resp, err = http.Post(ts.URL+"/api/agents/test-agent/souls", "application/json",
		strings.NewReader(`{"name":"../evil","content":""}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid name: expected 400, got %d", resp.StatusCode)
	}

	// List souls — should have one.
	resp, err = http.Get(ts.URL + "/api/agents/test-agent/souls")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(data, &listResp)
	souls = listResp["souls"].([]any)
	if len(souls) != 1 {
		t.Fatalf("expected 1 soul, got %d", len(souls))
	}
	soul := souls[0].(map[string]any)
	if soul["name"] != "friendly" {
		t.Fatalf("expected name=friendly, got %v", soul["name"])
	}
	if soul["active"].(bool) {
		t.Fatal("soul should not be active yet")
	}

	// Get soul content.
	resp, err = http.Get(ts.URL + "/api/agents/test-agent/souls/friendly")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get soul: got %d", resp.StatusCode)
	}
	var getResp map[string]any
	json.Unmarshal(data, &getResp)
	if getResp["content"] != "You are friendly." {
		t.Fatalf("unexpected content: %v", getResp["content"])
	}

	// Update soul content.
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/agents/test-agent/souls/friendly",
		strings.NewReader(`{"content":"You are very friendly."}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update soul: got %d", resp.StatusCode)
	}

	// Activate soul.
	resp, err = http.Post(ts.URL+"/api/agents/test-agent/souls/friendly/activate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("activate soul: got %d", resp.StatusCode)
	}

	// List should now show active=true.
	resp, err = http.Get(ts.URL + "/api/agents/test-agent/souls")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(data, &listResp)
	souls = listResp["souls"].([]any)
	soul = souls[0].(map[string]any)
	if !soul["active"].(bool) {
		t.Fatal("soul should be active after activate")
	}

	// Delete soul.
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/api/agents/test-agent/souls/friendly", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete soul: got %d", resp.StatusCode)
	}

	// List should be empty again.
	resp, err = http.Get(ts.URL + "/api/agents/test-agent/souls")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	json.Unmarshal(data, &listResp)
	souls = listResp["souls"].([]any)
	if len(souls) != 0 {
		t.Fatalf("expected 0 souls after delete, got %d", len(souls))
	}
}

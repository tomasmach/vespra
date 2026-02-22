package web_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[bot]\ntoken=\"x\"\n[llm]\napi_key=\"test\"\n"), 0o644); err != nil {
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

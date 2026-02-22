package web_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestSoulLibraryCRUD(t *testing.T) {
	ts, dir := newTestServer(t)

	// List — initially empty
	resp, _ := http.Get(ts.URL + "/api/soul-library")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list got %d", resp.StatusCode)
	}
	var list []map[string]any
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 0 {
		t.Fatalf("want empty list, got %v", list)
	}

	// Create
	body, _ := json.Marshal(map[string]string{"name": "friendly", "content": "You are friendly."})
	resp, _ = http.Post(ts.URL+"/api/soul-library", "application/json", bytes.NewReader(body))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify file on disk
	wantPath := filepath.Join(dir, "soul-library", "friendly.md")
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(data) != "You are friendly." {
		t.Fatalf("wrong content: %s", data)
	}

	// Get
	resp, _ = http.Get(ts.URL + "/api/soul-library/friendly")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get got %d", resp.StatusCode)
	}
	var got map[string]any
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got["content"] != "You are friendly." {
		t.Fatalf("wrong content: %v", got)
	}

	// Update
	body, _ = json.Marshal(map[string]string{"content": "You are very friendly."})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/soul-library/friendly", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update got %d", resp.StatusCode)
	}
	resp.Body.Close()
	data, _ = os.ReadFile(wantPath)
	if string(data) != "You are very friendly." {
		t.Fatalf("wrong content after update: %s", data)
	}

	// List — now has one entry
	resp, _ = http.Get(ts.URL + "/api/soul-library")
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 {
		t.Fatalf("want 1 entry, got %d", len(list))
	}
	if list[0]["name"] != "friendly" {
		t.Fatalf("wrong name: %v", list[0])
	}

	// Delete
	req, _ = http.NewRequest("DELETE", ts.URL+"/api/soul-library/friendly", nil)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatal("file should be deleted")
	}
}

func TestSoulLibraryInvalidName(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, name := range []string{"", "../evil", "has space", "x/y"} {
		body, _ := json.Marshal(map[string]string{"name": name, "content": "x"})
		resp, _ := http.Post(ts.URL+"/api/soul-library", "application/json", bytes.NewReader(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("name %q: want 400, got %d", name, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestActivateLibrarySoul(t *testing.T) {
	ts, dir := newTestServer(t)

	// Create a library soul first
	body, _ := json.Marshal(map[string]string{"name": "calm", "content": "You are calm."})
	resp, _ := http.Post(ts.URL+"/api/soul-library", "application/json", bytes.NewReader(body))
	resp.Body.Close()

	// Activate it as global soul
	body, _ = json.Marshal(map[string]string{"soul_library_name": "calm"})
	req, _ := http.NewRequest("PUT", ts.URL+"/api/soul", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Verify config was updated
	wantPath := filepath.Join(dir, "soul-library", "calm.md")
	cfgData, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
	if !bytes.Contains(cfgData, []byte(wantPath)) {
		t.Fatalf("config not updated with soul path; config:\n%s", cfgData)
	}
}

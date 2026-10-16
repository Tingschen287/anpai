package deckui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tingschen287/terminal-deck/work/internal/deckclient"
	"github.com/Tingschen287/terminal-deck/work/internal/deckstore"
)

// 自查补测（n057 attempt 3）：DeckSettingsAPI.Save 把读到的代数放进 PUT，并带回 deckd 回的、这次存下的代数。
func TestDeckSettingsAPISaveSendsAndReturnsGeneration(t *testing.T) {
	dir, err := os.MkdirTemp("", "dk") // unix socket 路径不能太长
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("建不了 unix socket：%v", err)
	}
	var sent map[string]any
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/v1/settings" {
			http.Error(w, "unexpected", 404)
			return
		}
		json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(deckstore.Settings{Generation: 1792044425151945})
	})}
	go srv.Serve(ln)
	defer srv.Close()
	api := DeckSettingsAPI{C: deckclient.New(sock)}
	gen, err := api.Save(context.Background(), deckstore.Settings{Generation: 1792044425151000, Agents: []deckstore.AgentDef{}, Projects: []deckstore.ProjectDef{}})
	if err != nil {
		t.Fatal(err)
	}
	if g, ok := sent["generation"].(float64); !ok || int(g) != 1792044425151000 {
		t.Fatalf("PUT 应带读到的代数：%v", sent["generation"])
	}
	if gen != 1792044425151945 {
		t.Fatalf("应带回存下的代数：%d", gen)
	}
}

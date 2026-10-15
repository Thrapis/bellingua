package main

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Thrapis/bellingua/internal/api"
	"github.com/Thrapis/bellingua/internal/backup"
	"github.com/Thrapis/bellingua/internal/config"
	"github.com/Thrapis/bellingua/internal/store"
)

// The bridge offers serve's tools, instructions included, and forwards calls.
func TestBridge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateProject(ctx, "demo", "ru", "be"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	bm, _ := backup.NewManager(st, config.Backup{}, log)
	ts := httptest.NewServer(api.New(ctx, st, config.Default(), nil, bm, nil, log).Handler())
	defer ts.Close()

	serverSide, clientSide := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- bridge(ctx, ts.URL+"/mcp", serverSide) }()

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cs.InitializeResult().Instructions, "bellingua") {
		t.Errorf("instructions = %q", cs.InitializeResult().Instructions)
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) < 10 {
		t.Fatalf("tools: %v %d", err, len(tools.Tools))
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_projects"})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"demo"`) {
		t.Errorf("list_projects = %s", text)
	}
	// Tool errors pass through as tool errors.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_unit", Arguments: map[string]any{"id": 42}})
	if err != nil || !res.IsError {
		t.Errorf("missing unit: %v %+v", err, res)
	}

	cs.Close()
	if err := <-done; err != nil {
		t.Errorf("bridge: %v", err)
	}
}

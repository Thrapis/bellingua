package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// cmdMCP serves the running bellingua's MCP tools over stdio, for clients
// that start their MCP servers as processes (Claude Desktop). The tools live
// in serve at /mcp; this only relays, because a second process writing the
// database would bypass serve's in-memory state (Rev, TM index, caches).
func cmdMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	load := commonFlags(fs)
	listen := fs.String("listen", "", "address bellingua serves on (overrides config)")
	fs.Parse(args)
	err := func() error {
		e, err := load()
		if err != nil {
			return err
		}
		if *listen != "" {
			e.cfg.Listen = *listen
		}
		url := localURL(e.cfg.Listen)
		if !isBellingua(url) {
			return fmt.Errorf("bellingua is not running at %s; start it first", url)
		}
		return bridge(ctx, url+"/mcp", &mcp.StdioTransport{})
	}()
	if err != nil && !errors.Is(err, context.Canceled) {
		// Not fail(): its "Press Enter" would read the client's stdin.
		fmt.Fprintf(os.Stderr, "bellingua mcp: %v\n", err)
		os.Exit(1)
	}
	return nil
}

// bridge connects to the MCP endpoint and offers its tools on t, forwarding
// every call, until t closes.
func bridge(ctx context.Context, endpoint string, t mcp.Transport) error {
	client := mcp.NewClient(&mcp.Implementation{Name: "bellingua-mcp", Version: version}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		return err
	}
	defer cs.Close()
	srv := mcp.NewServer(&mcp.Implementation{Name: "bellingua", Version: version},
		&mcp.ServerOptions{Instructions: cs.InitializeResult().Instructions, Capabilities: &mcp.ServerCapabilities{}})
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		name := tool.Name
		srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: req.Params.Arguments})
		})
	}
	return srv.Run(ctx, t)
}

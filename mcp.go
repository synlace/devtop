package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

type MCPServerConfig struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Env       []string `json:"env,omitempty"`
	URL       string   `json:"url,omitempty"`
}

type MCPConnection struct {
	ServerName string
	Client     *client.Client
	Tools      []mcp.Tool
}

var mcpConnections []*MCPConnection
var mcpMu sync.Mutex

func parseMCPServers() []MCPServerConfig {
	raw := os.Getenv("MCP_SERVERS")
	if raw == "" {
		return nil
	}
	var cfgs []MCPServerConfig
	if err := json.Unmarshal([]byte(raw), &cfgs); err != nil {
		fmt.Printf("Warning: failed to parse MCP_SERVERS: %v\n", err)
		return nil
	}
	return cfgs
}

func connectMCPServers(ctx context.Context, cfgs []MCPServerConfig) []*MCPConnection {
	if len(cfgs) == 0 {
		return nil
	}

	var conns []*MCPConnection
	for _, cfg := range cfgs {
		conn, err := connectOneMCP(ctx, cfg)
		if err != nil {
			fmt.Printf("Warning: failed to connect MCP server %q: %v\n", cfg.Name, err)
			continue
		}
		conns = append(conns, conn)
	}
	return conns
}

func connectOneMCP(ctx context.Context, cfg MCPServerConfig) (*MCPConnection, error) {
	var tr transport.Interface
	switch cfg.Transport {
	case "stdio":
		tr = transport.NewStdio(cfg.Command, cfg.Env, cfg.Args...)
	case "streamable-http":
		var err error
		tr, err = transport.NewStreamableHTTP(cfg.URL)
		if err != nil {
			return nil, fmt.Errorf("creating HTTP transport: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported transport %q (use stdio or streamable-http)", cfg.Transport)
	}

	c := client.NewClient(tr)
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting client: %w", err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "devtop",
		Version: "1.0.0",
	}
	initReq.Params.Capabilities = mcp.ClientCapabilities{}

	if _, err := c.Initialize(ctx, initReq); err != nil {
		c.Close()
		return nil, fmt.Errorf("initializing: %w", err)
	}

	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("listing tools: %w", err)
	}

	fmt.Printf("  MCP %q: %d tools discovered\n", cfg.Name, len(toolsResult.Tools))
	return &MCPConnection{
		ServerName: cfg.Name,
		Client:     c,
		Tools:      toolsResult.Tools,
	}, nil
}

func mcpToolToSchema(mt mcp.Tool) map[string]interface{} {
	props := make(map[string]interface{})
	required := []string{}

	inputSchema := mt.InputSchema
	if inputSchema.Type != "" {
		for k, v := range inputSchema.Properties {
			props[k] = v
		}
		required = inputSchema.Required
	}

	params := map[string]interface{}{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		params["required"] = required
	}

	return map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        mt.Name,
			"description": mt.Description,
			"parameters":  params,
		},
	}
}

func callMCPTool(serverName string, c *client.Client, toolName string, args map[string]interface{}) string {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	result, err := c.CallTool(ctx, req)
	if err != nil {
		return fmt.Sprintf("Error calling MCP tool %s/%s: %v", serverName, toolName, err)
	}

	var parts []string
	for _, content := range result.Content {
		switch v := content.(type) {
		case mcp.TextContent:
			parts = append(parts, v.Text)
		default:
			jsonBytes, err := json.Marshal(content)
			if err == nil {
				parts = append(parts, string(jsonBytes))
			}
		}
	}
	return strings.Join(parts, "\n")
}

func closeMCPConnections() {
	mcpMu.Lock()
	defer mcpMu.Unlock()
	for _, conn := range mcpConnections {
		if conn.Client != nil {
			conn.Client.Close()
		}
	}
	mcpConnections = nil
}
package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type executorFunc func(context.Context, string, app.Arguments) (string, error)

func (f executorFunc) Execute(ctx context.Context, name string, args app.Arguments) (string, error) {
	return f(ctx, name, args)
}

func TestMCPContract(t *testing.T) {
	calls := 0
	server := New(executorFunc(func(_ context.Context, name string, a app.Arguments) (string, error) {
		calls++
		if a.GroupURL == "fail" || (name == "list_folder_dialogs" && a.FolderID == 7) {
			return "", fmt.Errorf("Telegram unavailable")
		}
		return name, nil
	}))
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 14 {
		t.Fatalf("got %d tools", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		args := map[string]any{}
		switch tool.Name {
		case "list_folders":
		case "list_dialogs":
			args["archived"] = true
		case "list_folder_dialogs":
			args["folderId"] = 2
			args["limit"] = 50
		case "global_search":
			args["query"] = "hello"
		default:
			args["groupUrl"] = "@test"
			switch tool.Name {
			case "search_messages":
				args["query"] = "hello"
			case "fetch_since":
				args["sinceId"] = 0
			case "get_message", "get_media_info", "download_media", "get_thumbnail", "mark_read":
				args["messageId"] = 1
			}
		}
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: args})
		if err != nil || r.IsError || r.Content[0].(*mcp.TextContent).Text != tool.Name {
			t.Fatalf("%s: %+v, %v", tool.Name, r, err)
		}
		writes := tool.Name == "download_media" || tool.Name == "get_thumbnail" || tool.Name == "mark_read"
		if tool.Annotations.ReadOnlyHint == writes || tool.Annotations.IdempotentHint != (tool.Name == "mark_read") || *tool.Annotations.DestructiveHint {
			t.Errorf("incorrect annotation: %s", tool.Name)
		}
	}
	for _, args := range []map[string]any{{}, {"folderId": "2"}, {"folderId": 2.5}, {"folderId": 1}, {"folderId": 2147483648}, {"folderId": 2, "limit": 0}, {"folderId": 2, "unexpected": true}} {
		before := calls
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_folder_dialogs", Arguments: args})
		if err == nil && !r.IsError {
			t.Errorf("invalid folder request accepted: %+v", args)
		}
		if calls != before {
			t.Errorf("invalid folder request reached application: %+v", args)
		}
	}
	for _, args := range []map[string]any{{}, {"groupUrl": "x", "limit": 1.5}, {"groupUrl": "x", "limit": -1}, {"groupUrl": "x", "unexpected": true}, {"groupUrl": 123}} {
		before := calls
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_messages", Arguments: args})
		if err == nil && !r.IsError {
			t.Errorf("invalid input accepted: %+v", args)
		}
		if calls != before {
			t.Error("invalid input reached application")
		}
	}
	for _, args := range []map[string]any{{"groupUrl": "x"}, {"groupUrl": "x", "messageId": 12}} {
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "mark_read", Arguments: args})
		if err != nil || r.IsError {
			t.Errorf("valid mark_read rejected: %+v, %+v, %v", args, r, err)
		}
	}
	for _, args := range []map[string]any{{}, {"groupUrl": ""}, {"groupUrl": "x", "messageId": 0}, {"groupUrl": "x", "messageId": 1.5}, {"groupUrl": "x", "messageId": "1"}, {"groupUrl": "x", "unexpected": true}} {
		before := calls
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "mark_read", Arguments: args})
		if err == nil && !r.IsError {
			t.Errorf("invalid mark_read accepted: %+v", args)
		}
		if calls != before {
			t.Errorf("invalid mark_read reached application: %+v", args)
		}
	}
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_messages", Arguments: map[string]any{"groupUrl": "fail"}})
	if err != nil || !r.IsError {
		t.Fatalf("expected MCP tool error: %+v, %v", r, err)
	}
	r, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_folder_dialogs", Arguments: map[string]any{"folderId": 7}})
	if err != nil || !r.IsError {
		t.Fatalf("expected folder MCP tool error: %+v, %v", r, err)
	}
}

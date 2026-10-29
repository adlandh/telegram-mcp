package mcp

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type executorFunc func(context.Context, string, app.Arguments) (string, error)

func (f executorFunc) Execute(ctx context.Context, name string, args app.Arguments) (string, error) {
	return f(ctx, name, args)
}

var jpeg = []byte("\xff\xd8\xff\xe0")

// ViewThumbnail reuses f so tests can count calls and inject failures.
func (f executorFunc) ViewThumbnail(ctx context.Context, args app.Arguments) (domain.Image, error) {
	if _, err := f(ctx, "view_thumbnail", args); err != nil {
		return domain.Image{}, err
	}
	return domain.Image{Data: jpeg, MIME: "image/jpeg"}, nil
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
	if len(tools.Tools) != 17 {
		t.Fatalf("got %d tools", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		args := map[string]any{}
		switch tool.Name {
		case "list_folders", "get_me":
		case "list_dialogs":
			args["archived"] = true
			args["unreadOnly"] = true
		case "list_folder_dialogs":
			args["folderId"] = 2
			args["limit"] = 50
			args["unreadOnly"] = true
		case "global_search":
			args["query"] = "hello"
		default:
			args["groupUrl"] = "@test"
			switch tool.Name {
			case "read_messages":
				args["unreadOnly"] = true
			case "search_messages":
				args["query"] = "hello"
			case "fetch_since":
				args["sinceId"] = 0
			case "get_message", "get_media_info", "download_media", "get_thumbnail", "view_thumbnail", "mark_read":
				args["messageId"] = 1
			case "save_draft":
				args["text"] = "See you at 5"
			}
		}
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: args})
		if tool.Name == "view_thumbnail" {
			img, ok := r.Content[0].(*mcp.ImageContent)
			if err != nil || r.IsError || len(r.Content) != 2 || !ok || !bytes.Equal(img.Data, jpeg) || img.MIMEType != "image/jpeg" || r.Content[1].(*mcp.TextContent).Text != "Preview: image/jpeg, 0.0 KB" {
				t.Fatalf("%s: %+v, %v", tool.Name, r, err)
			}
		} else if err != nil || r.IsError || r.Content[0].(*mcp.TextContent).Text != tool.Name {
			t.Fatalf("%s: %+v, %v", tool.Name, r, err)
		}
		writes := slices.Contains([]string{"download_media", "get_thumbnail", "mark_read", "save_draft"}, tool.Name)
		idempotent := tool.Name == "mark_read" || tool.Name == "save_draft"
		if tool.Annotations.ReadOnlyHint == writes || tool.Annotations.IdempotentHint != idempotent || *tool.Annotations.DestructiveHint != (tool.Name == "save_draft") {
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
	for _, name := range []string{"read_messages", "search_messages"} {
		args := func(before any) map[string]any {
			a := map[string]any{"groupUrl": "x", "beforeId": before}
			if name == "search_messages" {
				a["query"] = "q"
			}
			return a
		}
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args(5)})
		if err != nil || r.IsError {
			t.Errorf("valid %s beforeId rejected: %+v, %v", name, r, err)
		}
		for _, before := range []any{0, -1, 1.5, 2147483648} {
			calls0 := calls
			r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args(before)})
			if err == nil && !r.IsError {
				t.Errorf("invalid %s beforeId accepted: %v", name, before)
			}
			if calls != calls0 {
				t.Errorf("invalid %s beforeId reached application: %v", name, before)
			}
		}
	}
	for _, tool := range tools.Tools {
		if _, ok := tool.InputSchema.(map[string]any)["properties"].(map[string]any)["maxTextChars"]; ok != slices.Contains([]string{"read_messages", "search_messages", "fetch_since", "get_pinned", "global_search"}, tool.Name) {
			t.Errorf("%s: unexpected maxTextChars presence %t", tool.Name, ok)
		}
	}
	for _, tool := range tools.Tools {
		p, ok := tool.InputSchema.(map[string]any)["properties"].(map[string]any)["unreadOnly"]
		if want := slices.Contains([]string{"read_messages", "list_dialogs", "list_folder_dialogs"}, tool.Name); ok != want || (ok && p.(map[string]any)["type"] != "boolean") {
			t.Errorf("%s: unreadOnly schema %v", tool.Name, p)
		}
	}
	for _, args := range []map[string]any{{"unreadOnly": "yes"}, {"unreadOnly": 1}, {"folderId": 2, "unreadOnly": "yes"}, {"folderId": 2, "unreadOnly": 1}, {"groupUrl": "@test", "unreadOnly": "yes"}, {"groupUrl": "@test", "unreadOnly": 1}} {
		name := "list_dialogs"
		if _, ok := args["folderId"]; ok {
			name = "list_folder_dialogs"
		}
		if _, ok := args["groupUrl"]; ok {
			name = "read_messages"
		}
		before := calls
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if (err == nil && !r.IsError) || calls != before {
			t.Errorf("invalid %s unreadOnly accepted: %+v", name, args)
		}
	}
	for i, args := range []map[string]any{{"groupUrl": "x"}, {"groupUrl": "x", "text": ""}, {"groupUrl": "x", "text": strings.Repeat("я", 4097)}, {"groupUrl": "x", "text": 5}, {"text": "hi"}, {"groupUrl": "x", "text": "hi", "unexpected": true}} {
		before := calls
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "save_draft", Arguments: args})
		if (err == nil && !r.IsError) || calls != before {
			t.Errorf("invalid save_draft case %d accepted or reached application", i)
		}
	}
	if r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "save_draft", Arguments: map[string]any{"groupUrl": "x", "text": strings.Repeat("я", 4096)}}); err != nil || r.IsError {
		t.Errorf("4096-character draft rejected: %+v, %v", r, err)
	}
	before := calls
	if r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_me", Arguments: map[string]any{"groupUrl": "x"}}); (err == nil && !r.IsError) || calls != before {
		t.Error("get_me accepted an unknown property")
	}
	for _, limit := range []any{500, 0, -1, 1.5} {
		before := calls
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_messages", Arguments: map[string]any{"groupUrl": "x", "maxTextChars": limit}})
		if valid := limit == 500; valid != (err == nil && !r.IsError) || valid != (calls != before) {
			t.Errorf("maxTextChars %v: valid=%t, result %+v, %v", limit, valid, r, err)
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
	for _, args := range []map[string]any{{"groupUrl": "x"}, {"groupUrl": "x", "messageId": 0}, {"groupUrl": "x", "messageId": 1, "maxMB": 1}} {
		before := calls
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "view_thumbnail", Arguments: args})
		if err == nil && !r.IsError {
			t.Errorf("invalid view_thumbnail accepted: %+v", args)
		}
		if calls != before {
			t.Errorf("invalid view_thumbnail reached application: %+v", args)
		}
	}
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "view_thumbnail", Arguments: map[string]any{"groupUrl": "fail", "messageId": 1}})
	if err != nil || !r.IsError || len(r.Content) != 1 {
		t.Fatalf("expected view_thumbnail tool error: %+v, %v", r, err)
	}
	r, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "read_messages", Arguments: map[string]any{"groupUrl": "fail"}})
	if err != nil || !r.IsError {
		t.Fatalf("expected MCP tool error: %+v, %v", r, err)
	}
	r, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_folder_dialogs", Arguments: map[string]any{"folderId": 7}})
	if err != nil || !r.IsError {
		t.Fatalf("expected folder MCP tool error: %+v, %v", r, err)
	}
}

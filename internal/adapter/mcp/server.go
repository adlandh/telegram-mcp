// Package mcp is the incoming stdio MCP adapter.
package mcp

import (
	"context"
	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Executor is the application's incoming port.
type Executor interface {
	Execute(context.Context, string, app.Arguments) (string, error)
}

func New(service Executor) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "telegram-user", Version: "1.0.0"}, nil)
	for _, def := range []struct {
		name, description  string
		required, optional []string
	}{
		{"read_messages", "Read recent messages, including album and reply links (default 50, max 200).", []string{"groupUrl"}, []string{"limit"}},
		{"search_messages", "Search within a chat (default 30, max 100).", []string{"groupUrl", "query"}, []string{"limit"}},
		{"get_group_info", "Get chat title, ID, username, member count and description.", []string{"groupUrl"}, nil},
		{"fetch_since", "Read messages newer than sinceId, oldest first, with a safe next maxId cursor (default 100, max 200).", []string{"groupUrl", "sinceId"}, []string{"limit"}},
		{"get_message", "Get a message and its engagement, album, reply and media metadata.", []string{"groupUrl", "messageId"}, nil},
		{"global_search", "Search messages across all chats (default 30, max 100).", []string{"query"}, []string{"limit"}},
		{"list_dialogs", "List chats with marked IDs and unread counts (default 100, max 500). archived=true selects archived chats only, matching the original service.", nil, []string{"limit", "archived"}},
		{"list_folders", "List configured Telegram chat folders.", nil, nil},
		{"list_folder_dialogs", "List chats in a custom folder by folderId from list_folders (default 100, max 500).", []string{"folderId"}, []string{"limit"}},
		{"get_pinned", "Read pinned messages (default 20, max 50).", []string{"groupUrl"}, []string{"limit"}},
		{"get_media_info", "Inspect media without downloading it.", []string{"groupUrl", "messageId"}, nil},
		{"download_media", "Download media to a local file. maxMB can only lower the configured limit; 0 or omitted uses it.", []string{"groupUrl", "messageId"}, []string{"maxMB"}},
		{"get_thumbnail", "Download only a media preview to a local file.", []string{"groupUrl", "messageId"}, nil},
		{"mark_read", "Mark messages as read in Telegram (changes account state). Telegram tracks a read cursor, so messageId marks every message up to and including it; omit it to mark the whole chat read.", []string{"groupUrl"}, []string{"messageId"}},
	} {
		properties := map[string]any{}
		for _, key := range append(append([]string{}, def.required...), def.optional...) {
			p := map[string]any{"type": "integer", "minimum": 1}
			switch key {
			case "groupUrl":
				p = map[string]any{"type": "string", "minLength": 1, "description": "https://t.me/name, @username, or marked numeric ID from list_dialogs"}
			case "query":
				p = map[string]any{"type": "string", "minLength": 1}
			case "archived":
				p = map[string]any{"type": "boolean"}
			case "sinceId", "maxMB":
				p["minimum"] = 0
			case "folderId":
				p["minimum"] = 2
				p["maximum"] = 2147483647
			}
			properties[key] = p
		}
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(def.required) > 0 {
			schema["required"] = def.required
		}
		readOnly := def.name != "download_media" && def.name != "get_thumbnail" && def.name != "mark_read"
		mcp.AddTool(server, &mcp.Tool{Name: def.name, Description: def.description, InputSchema: schema,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: def.name == "mark_read", DestructiveHint: new(false), OpenWorldHint: new(true)}},
			func(ctx context.Context, _ *mcp.CallToolRequest, args app.Arguments) (*mcp.CallToolResult, any, error) {
				text, err := service.Execute(ctx, def.name, args)
				if err != nil {
					return nil, nil, err
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
			})
	}
	return server
}

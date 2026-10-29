// Package mcp is the incoming stdio and HTTP MCP adapter.
package mcp

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/adlandh/telegram-mcp/internal/app"
	"github.com/adlandh/telegram-mcp/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Executor is the application's incoming port.
type Executor interface {
	Execute(context.Context, string, app.Arguments) (string, error)
	ViewThumbnail(context.Context, app.Arguments) (domain.Image, error)
}

func New(service Executor) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "telegram-user", Version: "1.0.0"}, nil)
	for _, def := range []struct {
		name, description  string
		required, optional []string
		readOnly           bool
	}{
		{"read_messages", "Read recent messages, including album and reply links (default 50, max 200). Pass the returned nextBeforeId as beforeId to page back through older history until empty. maxTextChars truncates each text, marked [truncated]; get_message returns it in full.", []string{"groupUrl"}, []string{"limit", "beforeId", "maxTextChars"}, true},
		{"search_messages", "Search within a chat (default 30, max 100). Pass the returned nextBeforeId as beforeId to page back through older matches until empty. maxTextChars truncates each text, marked [truncated]; get_message returns it in full.", []string{"groupUrl", "query"}, []string{"limit", "beforeId", "maxTextChars"}, true},
		{"get_group_info", "Get chat title, ID, username, member count and description.", []string{"groupUrl"}, nil, true},
		{"fetch_since", "Read messages newer than sinceId, oldest first, with a safe next maxId cursor (default 100, max 200). maxTextChars truncates each text, marked [truncated]; get_message returns it in full.", []string{"groupUrl", "sinceId"}, []string{"limit", "maxTextChars"}, true},
		{"get_message", "Get a message and its engagement, album, reply and media metadata.", []string{"groupUrl", "messageId"}, nil, true},
		{"global_search", "Search messages across all chats (default 30, max 100). maxTextChars truncates each text, marked [truncated]; get_message returns it in full.", []string{"query"}, []string{"limit", "maxTextChars"}, true},
		{"list_dialogs", "List chats with marked IDs and unread counts (default 100, max 500). archived=true selects archived chats only; false selects the main list.", nil, []string{"limit", "archived"}, true},
		{"list_folders", "List configured Telegram chat folders.", nil, nil, true},
		{"list_folder_dialogs", "List chats in a custom folder by folderId from list_folders (default 100, max 500).", []string{"folderId"}, []string{"limit"}, true},
		{"get_pinned", "Read pinned messages (default 20, max 50). maxTextChars truncates each text, marked [truncated]; get_message returns it in full.", []string{"groupUrl"}, []string{"limit", "maxTextChars"}, true},
		{"get_media_info", "Inspect media without downloading it.", []string{"groupUrl", "messageId"}, nil, true},
		{"download_media", "Download media to a local file. maxMB can only lower the configured limit; 0 or omitted uses it.", []string{"groupUrl", "messageId"}, []string{"maxMB"}, false},
		{"get_thumbnail", "Download only a media preview to a local file.", []string{"groupUrl", "messageId"}, nil, false},
		{"view_thumbnail", "Return a media preview as an inline image without writing a file; works for remote clients.", []string{"groupUrl", "messageId"}, nil, true},
		{"mark_read", "Mark messages as read in Telegram (changes account state). Telegram tracks a read cursor, so messageId marks every message up to and including it; omit it to mark the whole chat read.", []string{"groupUrl"}, []string{"messageId"}, false},
	} {
		properties := map[string]any{}
		for _, key := range slices.Concat(def.required, def.optional) {
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
				p["maximum"] = math.MaxInt32
			case "beforeId":
				p["maximum"] = math.MaxInt32
			}
			properties[key] = p
		}
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(def.required) > 0 {
			schema["required"] = def.required
		}
		mcp.AddTool(server, &mcp.Tool{Name: def.name, Description: def.description, InputSchema: schema,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: def.readOnly, IdempotentHint: def.name == "mark_read", DestructiveHint: new(false), OpenWorldHint: new(true)}},
			func(ctx context.Context, _ *mcp.CallToolRequest, args app.Arguments) (*mcp.CallToolResult, any, error) {
				if def.name == "view_thumbnail" {
					img, err := service.ViewThumbnail(ctx, args)
					if err != nil {
						return nil, nil, err
					}
					return &mcp.CallToolResult{Content: []mcp.Content{
						&mcp.ImageContent{Data: img.Data, MIMEType: img.MIME},
						&mcp.TextContent{Text: fmt.Sprintf("Preview: %s, %.1f KB", img.MIME, float64(len(img.Data))/1024)},
					}}, nil, nil
				}
				text, err := service.Execute(ctx, def.name, args)
				if err != nil {
					return nil, nil, err
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
			})
	}
	return server
}

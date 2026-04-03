package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"cc-bot/internal/telegram"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerTools(s *server.MCPServer, tg *telegram.BotClient) {
	s.AddTool(
		mcplib.NewTool("get_messages",
			mcplib.WithDescription("Get new Telegram messages since last check. Returns JSON array of messages."),
		),
		func(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			msgs := tg.DrainMessages()
			if len(msgs) == 0 {
				return mcplib.NewToolResultText("[]"), nil
			}
			data, _ := json.Marshal(msgs)
			return mcplib.NewToolResultText(string(data)), nil
		},
	)

	s.AddTool(
		mcplib.NewTool("reply",
			mcplib.WithDescription("Send a message to a Telegram chat."),
			mcplib.WithString("chat_id", mcplib.Required(), mcplib.Description("Telegram chat ID")),
			mcplib.WithString("text", mcplib.Required(), mcplib.Description("Message text (HTML supported)")),
			mcplib.WithString("reply_to", mcplib.Description("Message ID to thread under (optional)")),
			mcplib.WithArray("files", mcplib.Description("Absolute file paths to send as attachments (optional)")),
		),
		func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			chatIDStr, err := req.RequireString("chat_id")
			if err != nil {
				return mcplib.NewToolResultError("chat_id is required"), nil
			}
			text, err := req.RequireString("text")
			if err != nil {
				return mcplib.NewToolResultError("text is required"), nil
			}

			chatID, err := parseInt64(chatIDStr)
			if err != nil {
				return mcplib.NewToolResultError("invalid chat_id: " + chatIDStr), nil
			}

			replyTo := 0
			if rt := req.GetString("reply_to", ""); rt != "" {
				replyTo = req.GetInt("reply_to", 0)
			}

			files := getStringArray(req, "files")
			for _, filePath := range files {
				if _, err := tg.SendFile(chatID, filePath, "", replyTo); err != nil {
					return mcplib.NewToolResultError(fmt.Sprintf("send file %s failed: %v", filePath, err)), nil
				}
			}

			if text != "" || len(files) == 0 {
				if _, err := tg.SendMessage(chatID, text, replyTo); err != nil {
					return mcplib.NewToolResultError(fmt.Sprintf("send failed: %v", err)), nil
				}
			}

			return mcplib.NewToolResultText("sent"), nil
		},
	)

	s.AddTool(
		mcplib.NewTool("edit_message",
			mcplib.WithDescription("Edit a previously sent text message."),
			mcplib.WithString("chat_id", mcplib.Required(), mcplib.Description("Telegram chat ID")),
			mcplib.WithString("message_id", mcplib.Required(), mcplib.Description("Message ID to edit")),
			mcplib.WithString("text", mcplib.Required(), mcplib.Description("New message text (HTML supported)")),
		),
		func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			chatIDStr, err := req.RequireString("chat_id")
			if err != nil {
				return mcplib.NewToolResultError("chat_id is required"), nil
			}
			msgIDStr, err := req.RequireString("message_id")
			if err != nil {
				return mcplib.NewToolResultError("message_id is required"), nil
			}
			text, err := req.RequireString("text")
			if err != nil {
				return mcplib.NewToolResultError("text is required"), nil
			}

			chatID, err := parseInt64(chatIDStr)
			if err != nil {
				return mcplib.NewToolResultError("invalid chat_id"), nil
			}
			msgID, err := parseInt64(msgIDStr)
			if err != nil {
				return mcplib.NewToolResultError("invalid message_id"), nil
			}

			if err := tg.EditMessage(chatID, int(msgID), text); err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("edit failed: %v", err)), nil
			}

			return mcplib.NewToolResultText("edited"), nil
		},
	)

	s.AddTool(
		mcplib.NewTool("download_attachment",
			mcplib.WithDescription("Download a Telegram attachment by file_id. Returns the local file path."),
			mcplib.WithString("file_id", mcplib.Required(), mcplib.Description("Telegram file ID from an incoming message")),
		),
		func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			fileID, err := req.RequireString("file_id")
			if err != nil {
				return mcplib.NewToolResultError("file_id is required"), nil
			}

			localPath, err := tg.DownloadToTemp(fileID)
			if err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("download failed: %v", err)), nil
			}

			return mcplib.NewToolResultText(localPath), nil
		},
	)

	s.AddTool(
		mcplib.NewTool("react",
			mcplib.WithDescription("React to a Telegram message with an emoji."),
			mcplib.WithString("chat_id", mcplib.Required(), mcplib.Description("Telegram chat ID")),
			mcplib.WithString("message_id", mcplib.Required(), mcplib.Description("Message ID to react to")),
			mcplib.WithString("emoji", mcplib.Required(), mcplib.Description("Emoji to react with")),
		),
		func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
			chatIDStr, err := req.RequireString("chat_id")
			if err != nil {
				return mcplib.NewToolResultError("chat_id is required"), nil
			}
			msgIDStr, err := req.RequireString("message_id")
			if err != nil {
				return mcplib.NewToolResultError("message_id is required"), nil
			}
			emoji, err := req.RequireString("emoji")
			if err != nil {
				return mcplib.NewToolResultError("emoji is required"), nil
			}

			chatID, err := parseInt64(chatIDStr)
			if err != nil {
				return mcplib.NewToolResultError("invalid chat_id"), nil
			}
			msgID, err := parseInt64(msgIDStr)
			if err != nil {
				return mcplib.NewToolResultError("invalid message_id"), nil
			}

			if err := tg.SetReaction(chatID, int(msgID), emoji); err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("react failed: %v", err)), nil
			}

			return mcplib.NewToolResultText("reacted"), nil
		},
	)
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func getStringArray(req mcplib.CallToolRequest, key string) []string {
	raw, ok := req.GetArguments()[key]
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var result []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

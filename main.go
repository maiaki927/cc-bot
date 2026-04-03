package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cc-bot/internal/telegram"

	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	_ = godotenv.Load()
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is required")
	}

	tg := telegram.NewClient(token)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start Telegram long polling in background.
	go tg.StartPolling(ctx)

	// Start relay file watcher (reads messages written by userbot).
	go tg.StartRelayWatcher(ctx)

	// Hooks: inject experimental channel capability into initialize response.
	hooks := &server.Hooks{}
	hooks.AddAfterInitialize(func(_ context.Context, _ any, _ *mcp.InitializeRequest, result *mcp.InitializeResult) {
		result.Capabilities.Experimental = map[string]any{
			"claude/channel": map[string]any{},
		}
	})

	// Create MCP server.
	s := server.NewMCPServer(
		"cc-bot",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithHooks(hooks),
		server.WithInstructions(
			"The sender reads Telegram, not this session. Anything you want them to see must go through the reply tool — your transcript output never reaches their chat.\n\n"+
				"Messages from Telegram arrive as <channel source=\"telegram\" chat_id=\"...\" message_id=\"...\" user=\"...\" ts=\"...\">. "+
				"If the tag has an image_path attribute, Read that file — it is a photo the sender attached. "+
				"If the tag has attachment_file_id, call download_attachment with that file_id to fetch the file, then Read the returned path. "+
				"Reply with the reply tool — pass chat_id back. Use reply_to (set to a message_id) only when replying to an earlier message; the latest message doesn't need a quote-reply, omit reply_to for normal responses.\n\n"+
				"reply accepts file paths (files: [\"abs/path.png\"]) for attachments. Use react to add emoji reactions, and edit_message for interim progress updates. "+
				"Edits don't trigger push notifications — when a long task completes, send a new reply so the user's device pings.\n\n"+
				"Telegram's Bot API exposes no history or search — you only see messages as they arrive. If you need earlier context, ask the user to paste it or summarize.",
		),
	)

	registerTools(s, tg)

	// Push incoming Telegram messages to Claude via MCP channel notification.
	tg.OnMessage(func(msg telegram.Message) {
		log.Printf("[telegram] message from %s (chat %d): %s", msg.Username, msg.ChatID, truncate(msg.Text, 60))
		meta := map[string]any{
			"chat_id":    fmt.Sprintf("%d", msg.ChatID),
			"message_id": fmt.Sprintf("%d", msg.MessageID),
			"user":       msg.Username,
			"user_id":    fmt.Sprintf("%d", msg.UserID),
			"ts":         time.Unix(msg.Date, 0).UTC().Format(time.RFC3339),
		}
		if msg.ImagePath != "" {
			meta["image_path"] = msg.ImagePath
		}
		if msg.AttachmentFileID != "" {
			meta["attachment_file_id"] = msg.AttachmentFileID
		}
		s.SendNotificationToAllClients("notifications/claude/channel", map[string]any{
			"content": msg.Text,
			"meta":    meta,
		})
	})

	// Serve MCP: choose transport based on MCP_TRANSPORT env var.
	transport := strings.ToLower(os.Getenv("MCP_TRANSPORT"))
	switch transport {
	case "http":
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		addr := ":" + port

		authToken := os.Getenv("MCP_AUTH_TOKEN")

		httpServer := server.NewStreamableHTTPServer(s,
			server.WithHeartbeatInterval(30*time.Second),
		)

		// Wrap with auth middleware if token is configured.
		var handler http.Handler = httpServer
		if authToken != "" {
			handler = authMiddleware(authToken, httpServer)
		}

		mux := http.NewServeMux()
		mux.Handle("/mcp", handler)

		srv := &http.Server{
			Addr:    addr,
			Handler: mux,
		}

		// Graceful shutdown on context cancellation.
		go func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				log.Printf("http server shutdown: %v", err)
			}
		}()

		log.Printf("MCP StreamableHTTP server listening on %s/mcp", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server: %v", err)
		}

	default:
		// stdio transport (default).
		if err := server.ServeStdio(s); err != nil {
			log.Fatalf("mcp server: %v", err)
		}
	}
}

// authMiddleware returns an HTTP handler that validates Bearer token authentication.
func authMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + token
		if auth != expected {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func registerTools(s *server.MCPServer, tg *telegram.Client) {
	// --- get_messages: fetch buffered incoming messages ---
	s.AddTool(
		mcp.NewTool("get_messages",
			mcp.WithDescription("Get new Telegram messages since last check. Returns JSON array of messages."),
		),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			msgs := tg.DrainMessages()
			if len(msgs) == 0 {
				return mcp.NewToolResultText("[]"), nil
			}
			data, _ := json.Marshal(msgs)
			return mcp.NewToolResultText(string(data)), nil
		},
	)

	// --- reply: send a message to a Telegram chat (supports file attachments) ---
	s.AddTool(
		mcp.NewTool("reply",
			mcp.WithDescription("Send a message to a Telegram chat."),
			mcp.WithString("chat_id", mcp.Required(), mcp.Description("Telegram chat ID")),
			mcp.WithString("text", mcp.Required(), mcp.Description("Message text (HTML supported)")),
			mcp.WithString("reply_to", mcp.Description("Message ID to thread under (optional)")),
			mcp.WithArray("files", mcp.Description("Absolute file paths to send as attachments (optional)")),
		),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			chatIDStr, err := req.RequireString("chat_id")
			if err != nil {
				return mcp.NewToolResultError("chat_id is required"), nil
			}
			text, err := req.RequireString("text")
			if err != nil {
				return mcp.NewToolResultError("text is required"), nil
			}

			chatID, err := parseInt64(chatIDStr)
			if err != nil {
				return mcp.NewToolResultError("invalid chat_id: " + chatIDStr), nil
			}

			replyTo := 0
			if rt := req.GetString("reply_to", ""); rt != "" {
				replyTo = req.GetInt("reply_to", 0)
			}

			// Send file attachments if provided.
			files := getStringArray(req, "files")
			for _, filePath := range files {
				msgID, err := tg.SendFile(chatID, filePath, "", replyTo)
				if err != nil {
					return mcp.NewToolResultError(fmt.Sprintf("send file %s failed: %v", filePath, err)), nil
				}
				_ = msgID
			}

			// Send text message (skip if text is empty and files were sent).
			if text != "" || len(files) == 0 {
				msgID, err := tg.SendMessage(chatID, text, replyTo)
				if err != nil {
					return mcp.NewToolResultError(fmt.Sprintf("send failed: %v", err)), nil
				}
				_ = msgID
			}

			return mcp.NewToolResultText("sent"), nil
		},
	)

	// --- edit_message: edit a previously sent message ---
	s.AddTool(
		mcp.NewTool("edit_message",
			mcp.WithDescription("Edit a previously sent text message."),
			mcp.WithString("chat_id", mcp.Required(), mcp.Description("Telegram chat ID")),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID to edit")),
			mcp.WithString("text", mcp.Required(), mcp.Description("New message text (HTML supported)")),
		),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			chatIDStr, err := req.RequireString("chat_id")
			if err != nil {
				return mcp.NewToolResultError("chat_id is required"), nil
			}
			msgIDStr, err := req.RequireString("message_id")
			if err != nil {
				return mcp.NewToolResultError("message_id is required"), nil
			}
			text, err := req.RequireString("text")
			if err != nil {
				return mcp.NewToolResultError("text is required"), nil
			}

			chatID, err := parseInt64(chatIDStr)
			if err != nil {
				return mcp.NewToolResultError("invalid chat_id"), nil
			}
			msgID, err := parseInt64(msgIDStr)
			if err != nil {
				return mcp.NewToolResultError("invalid message_id"), nil
			}

			if err := tg.EditMessage(chatID, int(msgID), text); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("edit failed: %v", err)), nil
			}

			return mcp.NewToolResultText("edited"), nil
		},
	)

	// --- download_attachment: download a file from Telegram by file_id ---
	s.AddTool(
		mcp.NewTool("download_attachment",
			mcp.WithDescription("Download a Telegram attachment by file_id. Returns the local file path."),
			mcp.WithString("file_id", mcp.Required(), mcp.Description("Telegram file ID from an incoming message")),
		),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			fileID, err := req.RequireString("file_id")
			if err != nil {
				return mcp.NewToolResultError("file_id is required"), nil
			}

			localPath, err := tg.DownloadToTemp(fileID)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("download failed: %v", err)), nil
			}

			return mcp.NewToolResultText(localPath), nil
		},
	)

	// --- react: add emoji reaction to a message ---
	s.AddTool(
		mcp.NewTool("react",
			mcp.WithDescription("React to a Telegram message with an emoji."),
			mcp.WithString("chat_id", mcp.Required(), mcp.Description("Telegram chat ID")),
			mcp.WithString("message_id", mcp.Required(), mcp.Description("Message ID to react to")),
			mcp.WithString("emoji", mcp.Required(), mcp.Description("Emoji to react with")),
		),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			chatIDStr, err := req.RequireString("chat_id")
			if err != nil {
				return mcp.NewToolResultError("chat_id is required"), nil
			}
			msgIDStr, err := req.RequireString("message_id")
			if err != nil {
				return mcp.NewToolResultError("message_id is required"), nil
			}
			emoji, err := req.RequireString("emoji")
			if err != nil {
				return mcp.NewToolResultError("emoji is required"), nil
			}

			chatID, err := parseInt64(chatIDStr)
			if err != nil {
				return mcp.NewToolResultError("invalid chat_id"), nil
			}
			msgID, err := parseInt64(msgIDStr)
			if err != nil {
				return mcp.NewToolResultError("invalid message_id"), nil
			}

			if err := tg.SetReaction(chatID, int(msgID), emoji); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("react failed: %v", err)), nil
			}

			return mcp.NewToolResultText("reacted"), nil
		},
	)
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func getStringArray(req mcp.CallToolRequest, key string) []string {
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

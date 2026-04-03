package mcp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"cc-bot/internal/config"
	"cc-bot/internal/telegram"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const _instructions = "The sender reads Telegram, not this session. Anything you want them to see must go through the reply tool — your transcript output never reaches their chat.\n\n" +
	"Messages from Telegram arrive as <channel source=\"telegram\" chat_id=\"...\" message_id=\"...\" user=\"...\" ts=\"...\">. " +
	"If the tag has an image_path attribute, Read that file — it is a photo the sender attached. " +
	"If the tag has attachment_file_id, call download_attachment with that file_id to fetch the file, then Read the returned path. " +
	"Reply with the reply tool — pass chat_id back. Use reply_to (set to a message_id) only when replying to an earlier message; the latest message doesn't need a quote-reply, omit reply_to for normal responses.\n\n" +
	"reply accepts file paths (files: [\"abs/path.png\"]) for attachments. Use react to add emoji reactions, and edit_message for interim progress updates. " +
	"Edits don't trigger push notifications — when a long task completes, send a new reply so the user's device pings.\n\n" +
	"Telegram's Bot API exposes no history or search — you only see messages as they arrive. If you need earlier context, ask the user to paste it or summarize."

// NewServer creates and configures a new MCP server with all tools registered.
func NewServer(tg telegram.Client) *server.MCPServer {
	hooks := &server.Hooks{}
	hooks.AddAfterInitialize(func(_ context.Context, _ any, _ *mcplib.InitializeRequest, result *mcplib.InitializeResult) {
		result.Capabilities.Experimental = map[string]any{
			"claude/channel": map[string]any{},
		}
	})

	s := server.NewMCPServer(
		"cc-bot",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithHooks(hooks),
		server.WithInstructions(_instructions),
	)

	registerTools(s, tg)
	return s
}

// SetupNotifications wires incoming Telegram messages to MCP channel notifications.
func SetupNotifications(s *server.MCPServer, tg telegram.Client) {
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
}

// Serve starts the MCP server using the configured transport.
func Serve(ctx context.Context, cfg *config.Config, s *server.MCPServer) error {
	switch cfg.Transport {
	case "http":
		return serveHTTP(ctx, cfg, s)
	default:
		return server.ServeStdio(s)
	}
}

func serveHTTP(ctx context.Context, cfg *config.Config, s *server.MCPServer) error {
	addr := ":" + cfg.Port

	httpServer := server.NewStreamableHTTPServer(s,
		server.WithHeartbeatInterval(30*time.Second),
	)

	var handler http.Handler = httpServer
	if cfg.AuthToken != "" {
		handler = authMiddleware(cfg.AuthToken, httpServer)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

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
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

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

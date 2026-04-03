package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"cc-bot/internal/config"
	mcpserver "cc-bot/internal/mcp"
	"cc-bot/internal/relay"
	"cc-bot/internal/telegram"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	tg := telegram.NewBotClient(cfg.TelegramToken, cfg.AllowedUserIDs)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Create MCP server.
	s := mcpserver.NewServer(tg)

	// Set up relay watcher with dedup — relay pushes messages into the
	// message buffer. MCP notifications are wired after.
	tg.FetchBotUserID()
	watcher := relay.NewWatcher(cfg.RelayFile, tg.BotUserID, func(msg telegram.Message) {
		tg.DispatchMessage(msg)
	})

	// Wire MCP channel notifications for both polling and relay messages.
	// SetupNotifications sets tg.OnMessage, which fires for poll AND relay
	// messages because the relay watcher's onMsg appends to the buffer and
	// then the watcher calls tg.onMsg via DispatchMessage.
	mcpserver.SetupNotifications(s, tg)

	// Poller marks messages as seen so the relay watcher skips them.
	tg.SetOnPollMessage(watcher.MarkSeen)

	go tg.StartPolling(ctx)
	go watcher.Start(ctx)

	if err := mcpserver.Serve(ctx, cfg, s); err != nil {
		log.Fatal(err)
	}
}

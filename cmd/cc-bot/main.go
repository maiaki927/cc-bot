package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"cc-bot/internal/config"
	mcpserver "cc-bot/internal/mcp"
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

	s := mcpserver.NewServer(tg)
	mcpserver.SetupNotifications(s, tg)

	go tg.StartPolling(ctx)

	if err := mcpserver.Serve(ctx, cfg, s); err != nil {
		log.Fatal(err)
	}
}

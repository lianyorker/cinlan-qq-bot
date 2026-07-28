//go:build !localbusiness

package main

import (
	"context"
	"log/slog"

	"github.com/lianyorker/cinlan-qq-bot/internal/bot"
	"github.com/lianyorker/cinlan-qq-bot/internal/config"
)

func registerLocalExtensions(
	_ context.Context,
	_ config.Config,
	_ *bot.Service,
	_ *slog.Logger,
) error {
	return nil
}

package tui

import (
    "context"
    "log/slog"

    "github.com/spf13/cobra"
)

type Config struct{}

func NewCommand() *cobra.Command {
    var cfg Config
    cmd := &cobra.Command{
        Use:   "tui",
        Short: "Terminal dashboard",
        RunE: func(cmd *cobra.Command, _ []string) error {
            return Run(cmd.Context(), cfg)
        },
    }
    return cmd
}

func Run(ctx context.Context, _ Config) error {
    slog.Info("starting")
    <-ctx.Done()
    slog.Info("shutting down")
    return nil
}


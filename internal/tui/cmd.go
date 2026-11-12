package tui

import (
    "context"
    "log/slog"
    "os"

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
    log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "tui")
    log.Info("starting")
    <-ctx.Done()
    log.Info("shutting down")
    return nil
}


package main

import (
    "context"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    "github.com/spf13/cobra"

    "anti-interrupt/internal/agent"
    "anti-interrupt/internal/schema"
    "anti-interrupt/internal/tui"
)

func main() {
    // One cancellation source for the whole process: Ctrl-C or SIGTERM.
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    root := &cobra.Command{
        Use:           "anti-interrupt [service]",
        Short:         "Anti-Interrupt Conference Call System",
        SilenceUsage:  true, // don't dump usage on every runtime error
        SilenceErrors: true, // we log errors ourselves
    }

    // Subcommands. Each lives in its own internal/ package.
    root.AddCommand(
        schema.NewCommand(),
        agent.NewCommand(),
        tui.NewCommand(),
    )

    if err := root.ExecuteContext(ctx); err != nil {
        slog.Error("Fatal error", "error", err)
        os.Exit(1)
    }
}

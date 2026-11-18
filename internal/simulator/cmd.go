package simulator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/spf13/cobra"
)

// Config holds everything the simulator needs, populated from flags.
type Config struct {
    Rooms        int
    UsersPerRoom int
    FrequencyHz  int
    Mode         string
}

// NewCommand builds the `app simulator` cobra command.
func NewCommand() *cobra.Command {
    var cfg Config

    cmd := &cobra.Command{
        Use:   "simulator",
        Short: "Call simulator (no Cassandra access)",
        RunE: func(cmd *cobra.Command, _ []string) error {
            return Run(cmd.Context(), cfg)
        },
    }

    cmd.Flags().IntVar(&cfg.Rooms, "rooms", 4, "number of rooms")
    cmd.Flags().IntVar(&cfg.UsersPerRoom, "users-per-room", 10, "users per room")
    cmd.Flags().IntVar(&cfg.FrequencyHz, "freq", 100, "events per second")
    cmd.Flags().StringVar(&cfg.Mode, "mode", "random", "races|fairness|random")

    return cmd
}

// Run is the real entry point for the simulator. For now it's a stub that
// blocks until cancellation, so we can verify the plumbing.
func Run(ctx context.Context, cfg Config) error {
    jsonLog := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "simulator")

	if !slices.Contains([]string{"random", "fairness", "races"}, cfg.Mode) {return fmt.Errorf("Unsupported mode type: %s", cfg.Mode)}

    jsonLog.Info("starting",
        "rooms", cfg.Rooms,
        "users_per_room", cfg.UsersPerRoom,
        "freq", cfg.FrequencyHz,
        "mode", cfg.Mode,
    )

    <-ctx.Done() // wait for Ctrl-C / SIGTERM
    slog.Info("shutting down")
    return nil
}


package tui

import (
	"anti-interrupt/internal/config"
	"context"
	"log/slog"

	"github.com/spf13/cobra"
)

type Config struct{}

func NewCommand() *cobra.Command {
    var cassCfg config.CassandraConfig
    cmd := &cobra.Command{
        Use:   "tui",
        Short: "Terminal dashboard",
        RunE: func(cmd *cobra.Command, _ []string) error {
            return Run(cmd.Context(), cassCfg)
        },
    }


	// Required Flag: Cassandra address
	defaultnodes := make([]string, 1)
	defaultnodes[0] = "127.0.0.1:9042"
	cmd.Flags().StringSliceVar(
		&cassCfg.Nodes, "nodes", defaultnodes, 
		"Cassandra cluster node address:port pair (repeatable)",
		)
	err := cmd.MarkFlagRequired("nodes"); if err != nil { panic(err) }

    return cmd
}

func Run(ctx context.Context, cfg config.CassandraConfig) error {
    slog.Info("starting")
    <-ctx.Done()
    slog.Info("shutting down")
    return nil
}


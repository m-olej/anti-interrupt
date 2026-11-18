package backend

import (
    "context"
    "log/slog"

    "github.com/spf13/cobra"

	"anti-interrupt/internal/schema"
)

type Config struct{
	host string
	port int
	nodes []string
	keyspace string
	rf int

}

func Validate(cmd *cobra.Command, args []string) error {
	// Validation logic goes here	
	return nil
}

func NewCommand() *cobra.Command {
	var cfg Config
    cmd := &cobra.Command{
        Use:   "backend",
        Short: "Backend (arbiter & auditor)",
		PreRunE: Validate,
        RunE: func(cmd *cobra.Command, _ []string) error {

			// Variable configuration
			cmd.Flags().StringVar(&cfg.host, "host", "127.0.0.1", "Address service will run on (localhost by default)")
			cmd.Flags().IntVar(&cfg.port, "port", 1234, "Port service will run on (1234 by default)")
			cmd.Flags().IntVar(&cfg.rf, "rf", 3, "Replication factor of the cassandra keyspace (3 by default)")
			
			defaultnodes := make([]string, 1)
			defaultnodes[0] = "127.0.0.1:9042"
			cmd.Flags().StringSliceVar(&cfg.nodes, "nodes", defaultnodes, "Cassandra cluster node address:port pair (repeatable)")

			// Constants
			cfg.keyspace = "anti_interrupt"
            return Run(cmd.Context(), cfg)
        },
    }
    return cmd
}

func Run(ctx context.Context, cfg Config) error {
    slog.Info("Starting")

	slog.Info("Running migrations")

	err := schema.BootstrapKeyspace(cfg.nodes, cfg.keyspace, cfg.rf); if err != nil {
		return err
	}
	schema.Migrate(cfg.nodes, cfg.keyspace)

    <-ctx.Done()
    slog.Info("Shutting down")
    return nil
}

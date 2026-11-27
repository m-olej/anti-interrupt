package agent

import (
	"anti-interrupt/internal/config"

	"github.com/gocql/gocql"
	"github.com/spf13/cobra"
)

func NewCommand() *cobra.Command {
	var agentCfg config.AgentConfig
	var cassCfg config.CassandraConfig
	var runCfg config.RunConfig

    cmd := &cobra.Command{
        Use:   "agent",
        Short: "Client application (oneshot or http server)",
        RunE: func(cmd *cobra.Command, _ []string) error {
            return Run(cmd.Context(), agentCfg, cassCfg)
        },
    }

	// Required Flag: Cassandra address
	defaultnodes := make([]string, 1)
	defaultnodes[0] = "127.0.0.1:9042"
	cmd.Flags().StringSliceVar(
		&cassCfg.Nodes, "nodes", defaultnodes, 
		"Cassandra cluster node address:port pair (repeatable)",
		)
	if err := cmd.MarkFlagRequired("nodes"); err != nil { panic(err) }


	// Agent server configuration
	cmd.Flags().StringVar(
		&agentCfg.Address, "host", "", 
		"Address service will run on (localhost by default)",
		)
	cmd.Flags().IntVar(
		&agentCfg.Port, "port", 1234, 
		"Port service will run on (1234 by default)",
		)	

	// One-shot configuration (required)
    cmd.Flags().IntVar(&runCfg.Rooms, "rooms", 4, "number of rooms")
    cmd.Flags().IntVar(&runCfg.UserPerRoom, "users-per-room", 10, "users per room")
    cmd.Flags().IntVar(&runCfg.Freq, "freq", 100, "events per second")
    cmd.Flags().StringVar(&runCfg.TestMode, "mode", "random", "races|fairness|random")
    cmd.Flags().IntVar(&runCfg.Duration, "duration", 15, "Duration of test (in seconds)")

	// One-shot configuration (additional)
	cmd.Flags().IntVar(&runCfg.ClockOffset, "offset", 0, "Maximum clock offset between clients (in milliseconds)")
	cmd.Flags().Int64Var(&runCfg.Seed, "seed", 0, "Pseudorandom sequence seed (0 for runtime generation)")
	cmd.Flags().StringVar(&runCfg.TelemetryCl, "telemetry_concistency", gocql.One.String(), "Telemetry consistency level")	

    return cmd
}

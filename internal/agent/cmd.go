package agent

import (
	"github.com/spf13/cobra"
	"anti-interrupt/internal/config"
)

func NewCommand() *cobra.Command {
	var agentCfg config.AgentConfig
	var cassCfg config.CassandraConfig

    cmd := &cobra.Command{
        Use:   "backend",
        Short: "Backend (arbiter & auditor)",
        RunE: func(cmd *cobra.Command, _ []string) error {
            return Run(cmd.Context(), agentCfg, cassCfg)
        },
    }

	// Variable configuration
	cmd.Flags().StringVar(
		&agentCfg.Address, "host", "127.0.0.1", 
		"Address service will run on (localhost by default)",
		)
	cmd.Flags().IntVar(
		&agentCfg.Port, "port", 1234, 
		"Port service will run on (1234 by default)",
		)	

	defaultnodes := make([]string, 1)
	defaultnodes[0] = "127.0.0.1:9042"
	cmd.Flags().StringSliceVar(
		&cassCfg.Nodes, "nodes", defaultnodes, 
		"Cassandra cluster node address:port pair (repeatable)",
		)

    return cmd
}

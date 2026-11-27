package cassandra

import (
	"anti-interrupt/internal/config"

	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3"
)

// Use single session with the same core config for all requests
func NewSession(cfg config.CassandraConfig) (gocqlx.Session, error) {
	
	connConfig := gocql.ClusterConfig{
		Keyspace: cfg.Keyspace,
		Hosts: cfg.Nodes, 				// Cluster address(es)
		PoolConfig: gocql.PoolConfig{
			// TokenAware -> when using preparedStatements requests go directly to the assigned node, 
			// 				 as opposed to being load_balanced by the coordinator (telemetry hot path optimization)
			// RoundRobin -> Fallback, when using custom queries (avoid) that don't include the routing key
			HostSelectionPolicy: gocql.TokenAwareHostPolicy(gocql.RoundRobinHostPolicy()),
		},
		NumConns: 4, // 4 sockets per cassandra node 
		MaxRequestsPerConn: 32768, // Sender buffer depth for each socket (default value)
	}
	sess, err := gocqlx.WrapSession(gocql.NewSession(connConfig))
	return sess, err
}

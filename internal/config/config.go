package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gocql/gocql"
)

// Precedence:
// 1. Explicit flags (cmdline)
// 2. Environment
// 4. Defaults

type Config interface {
	Validate() error
}

type TestModes int
const (
	Random TestModes = iota
	Races
	Fairness
	Skew
	Collission
	Timeout
	Audit
	Failure
	Scale
)

var testModes = map[TestModes]string{
	Random: "random",
	Races: "races",
	Fairness: "fairness",
	Skew: "skew",
	Collission: "collision",
	Timeout: "timeout",
	Audit: "audit",
	Failure: "failure",
	Scale: "scale",
}

/// Stress-test run
type RunConfig struct {
	// Identification
	Run_id string		// Generated string(uuid) or provided
	Client_id string 	// defaults to hostname

	// Parameters
	Rooms int
	User_per_room int
	Freq int
	Test_mode TestModes
	Duration int 		// in seconds

	// Additional parameters (have defaults)
	Clock_offset int	// in milliseconds
	Delay_ms int		// Claim acquisition stabilization window (Write -> wait -> Read)
	Seed int64
	Telemetry_cl gocql.Consistency

	// Client side request batching
	Batch_frames int 	// either or
	Batch_ms int		// ^
}

type AgentConfig struct {
	Address string
	Port int
}

type CassandraConfig struct {
	Nodes []string
	Replication_factor int
	Keyspace string
	Reset bool
}

// Types are validated by runtime, complex parsing here

func (cass CassandraConfig) Validate() error{

	if cass.Replication_factor <= 0 {
		return fmt.Errorf("Replication factor has to be positive")
	}

	if cass.Keyspace != "anti_interrupt" {
		return fmt.Errorf("Sike, you can't change the keyspace")
	}
	
	for node := range cass.Nodes {
		var curr string
		var addrPair []string

		curr = cass.Nodes[node]	
		addrPair = strings.Split(curr, ":")

		if len(addrPair) != 2 {
			return fmt.Errorf("invalid nodes format: %v", cass.Nodes)
		}

		port, err := strconv.Atoi(addrPair[1]); if err != nil && port >= 1023 {
			return fmt.Errorf("invalid nodes format: %v", cass.Nodes)
		}
	}	

	return nil
}

func (srv AgentConfig) Validate() error{
	
	return nil
}

func (runc RunConfig) Validate() error {
	
	return nil
}

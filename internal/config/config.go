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
	RunId string		// Generated string(uuid) or provided
	ClientId string 	// defaults to hostname

	// Parameters
	Rooms int
	UserPerRoom int
	Freq int
	TestMode string // TestModes
	Duration int 		// in seconds

	// Additional parameters (have defaults)
	ClockOffset int	// in milliseconds
	Seed int64
	TelemetryCl string
}

type AgentConfig struct {
	Address string
	Port int
	OneShot RunConfig
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

	if len(cass.Nodes) == 0 { return fmt.Errorf("No cassandra address provided") }
	
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

const (
	ONESHOT = 0x1
	AGENT = 0x2
)

// If host parameter is provided, one-shot parameters are ignored
func RunMode(cfg AgentConfig) uint16 {
	if cfg.Address != "" {
		return AGENT
	} else {
		return ONESHOT
	}
}

func (srv AgentConfig) Validate() error {

	switch RunMode(srv) {
	// Validate OneShot Config
	case ONESHOT:
		_, err := gocql.ParseConsistencyWrapper(srv.OneShot.TestMode); if err != nil {
			return fmt.Errorf("Invalid consistency parameter value: %s", srv.OneShot.TestMode)
		}

	// Validate Agent Config
	case AGENT:
		// Host & Port variables are typechecked by cobra
	}
		
	return nil
}

func (runc RunConfig) Validate() error {

	isValid := false
	for tm := range testModes {
		if testModes[tm] == runc.TestMode { isValid = true }
	}
	if !isValid { return fmt.Errorf("invalid test mode: %s", runc.TestMode) }
	
	return nil
}

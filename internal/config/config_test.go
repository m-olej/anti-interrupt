package config

import "testing"

// --- Unit tests --- //

func TestCassandraNodes(t *testing.T) {
	nodes := make([]string, 3)
	errNodes := make([]string, 3)

	nodes[0] = "127.0.0.1:9042"
	nodes[1] = "150.254.32.69:9042"
	nodes[2] = "192.168.0.100:9042"

	errNodes[0] = "127.0.0.1:9042"
	errNodes[1] = "150.254.32.69:100"
	errNodes[2] = "192.168.0.100:-9042"

	goodCass := CassandraConfig{
		Nodes: nodes,
		Replication_factor: 3,
		Keyspace: "anti_interrupt",
	}

	badCass := CassandraConfig{
		Nodes: errNodes,
		Replication_factor: -2,
	}
	
	err := goodCass.Validate(); if err == nil {
		t.Log("Correct cassandra configuration passes validation")
	} else {
		t.Error("goodCass validation error", err)
		t.Fail()
	}

	err = badCass.Validate(); if err != nil {
		t.Log("Incorrect cassandra configuration flags error")	
	} else {
		t.Fail()
	}
}


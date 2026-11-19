## Development Deployment

**Options**:
- Docker
- k3d (local kubernetes cluster)
    - K8ssandra operator
    - simple config
- ccm (cassandra cluster manager)

**Chosen**: Docker
**Why**: k3d only brings more complexity to the project, without real benefits as this is only a project cluster for development purposes, unnecessary pvc and K8ssandra operator (waaay overkill) aren't needed. CCM is the native local clusted cassandra deployment tool, but it requires installing additional packages and building cassandra from java source code, which is abnoxious.

### Compose

3 cassandra nodes with config:
```yaml
environment:
  - CASSANDRA_CLUSTER_NAME=anti-interrupt # sets the same cluster_name in each containers /etc/cassandra/conf/cassandra.yaml
  - CASSANDRA_SEEDS=cass1 # one seed in cluster
  # Stable dev environment  
  - MAX_HEAP_SIZE=128M
  - HEAP_NEWSIZE=24M
```

Data persistance is unnecessary as schema migrations, will be handled by **Go** and there is no point in keeping past testing data.

### Cluster-size
Given *replication_factor=3* in a 3 node cluster, each node will hold 100% of the partitions. This doesn't go against any project requirements as none of the assumptions act differently when all data is present in a single node.
More importantly with *RF=3* `QUORUM`, which is $/floor{RF/2} + 1$ tolerates a single node failure, which is needed to show *Simultaneous Speech Onset & Lock-Free Floor Contention (No LWT)* and *High-Frequency Acoustic Telemetry* assumptions.

## Project structure

The main goal is to limit friction, when running project in the lab. 

A single binary `app`, which can run all 3 services: *simulator*, *auditor*, *tui*

```
root/
├── go.mod                      # Go module declaration (+dependencies) -> single project
├── go.sum
├── Makefile                    # Script automation (building, cluster deployment, ...)
├── docker-compose.yml          # Cassandra
├── schema.cql                  # Cassandra table schema
├── README.md
│
├── cmd/
│   └── app/
│       └── main.go             # cobra (cmdline library) root, ≤60 lines
│
├── internal/                   
│   ├── config/                # Config struct + loaders (flags → env → defaults)
│   ├── cassandra/             # session factory, retry policy, idempotency helpers
│   ├── schema/                # CQL constants, DDL bootstrap
│   ├── domain/                # Room, User, SpeechTurn, FloorClaim, ...
│   ├── telemetry/             # audio_telemetry writes, TTL bucketing
│   ├── arbiter/               # floor-claim state machine (write-wait-read)
│   ├── auditor/               # token-range paginator, Statistics
│   ├── counter/               # room_call_counters + completed_speech_turns
│   ├── transport/             # HTTP/gRPC handlers (shared by simulator↔backend↔tui)
│   │# All 3 apps (cobra subcommands) can import above modules
│   ├── simulator/
│   │   ├── cmd.go             # cobra subcommand (app simulator)
│   │   ├── spawner.go
│   │   ├── scheduler.go       # races / fairness / random modes
│   │   └── logger.go
│   ├── backend/
│   │   └── cmd.go             # cobra subcommand (app auditor); wires arbiter + auditor
│   └── tui/
│       ├── cmd.go
│       ├── fetch.go
│       └── display.go
│
└── api/                        # optional: .proto files (might be nice to let the arbiter and tui communicate via gRPC for throughput maximalization :) )
└── v1/*.proto
```

--- 

# **Commands**:
- `backend`: start backend with default values

*Helpers*:
- `make`: build app
- `docker compose up -d --wait`: deploys cassandra container cluster, --wait signals to exit the command only after the last container has joined the cluster
- `make cqlsh`: connect to cassandra cluster
- `make connect`: connect to app keyspace

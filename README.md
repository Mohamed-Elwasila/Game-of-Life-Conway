# Distributed Game of Life

A distributed implementation of Conway's Game of Life in Go, using RPC to split the board across multiple worker nodes. Originally built for the University of Bristol Computer Systems A coursework (2025).

![System Overview](images/gol.drawio.png)

## How it works

- **Controller** (`main.go`, `gol/`) loads the initial board, sends it to the broker, and handles keypresses and SDL output.
- **Broker** (`broker/`) splits the board into horizontal strips and coordinates each turn across the workers.
- **Workers** (`worker/`) each own a strip of the board. Each turn they swap edge rows directly with their neighbours (halo exchange), then compute the next state using multiple goroutines.

Workers keep the actual board state. The broker only asks for the full board when it needs it, e.g. for snapshots or alive-cell counts. There's also a primary/backup broker setup with heartbeat-based leader election, so if the primary goes down a backup takes over.

## Running it

Requires Go and SDL2.

```bash
make start-services   # starts the broker and 4 workers locally
go run .              # runs the controller
make stop-services
```

Or start things manually:

```bash
go run ./broker -port 8030 -primary=true -id=broker1
go run ./worker -port 8031   # repeat for 8032, 8033, 8034
go run . -w 512 -h 512 -turns 1000
```

Controller flags: `-w`, `-h` (board size), `-t` (threads per worker), `-turns`, `-headless`.

Keys while running: `s` saves the current board as a PGM, `p` pauses/resumes, `q` quits.

Node addresses are set in `distributed/stubs.go` (defaults to localhost).

## Possible improvements

- Dynamic load balancing between workers
- Full Raft for the broker cluster instead of simple leader election
- Docker Compose setup for running across machines

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"sync"

	"uk.ac.bris.cs/gameoflife/distributed"
	"uk.ac.bris.cs/gameoflife/util"
)

type WorkerOps struct {
	mu          sync.RWMutex
	world       [][]byte // current state (read-only during turn processing)
	nextWorld   [][]byte // write buffer for next state
	topHalo     []byte
	bottomHalo  []byte
	imageWidth  int
	imageHeight int
	startY      int
	endY        int
	workerID    int
	threads     int
	currentTurn int
	neighbors   struct {
		top    string
		bottom string
	}
	neighborClients struct {
		topClient    *rpc.Client
		bottomClient *rpc.Client
		mu           sync.RWMutex
	}
	// Synchronization for halo exchange
	haloMu            sync.Mutex
	haloTurn          int
	topHaloReceived   bool
	bottomHaloReceived bool
	haloCond          *sync.Cond
}

func (w *WorkerOps) Init(req distributed.InitRequest, res *distributed.InitResponse) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.world = req.World
	w.imageWidth = req.ImageWidth
	w.imageHeight = req.ImageHeight
	w.startY = req.StartY
	w.endY = req.EndY
	w.workerID = req.WorkerID
	w.threads = req.Threads
	w.currentTurn = 0

	// Initialize nextWorld buffer
	w.nextWorld = make([][]byte, len(req.World))
	for i := range w.nextWorld {
		w.nextWorld[i] = make([]byte, w.imageWidth)
	}

	w.topHalo = make([]byte, w.imageWidth)
	w.bottomHalo = make([]byte, w.imageWidth)

	// Initialize halo synchronization
	w.haloCond = sync.NewCond(&w.haloMu)
	w.haloTurn = 0
	w.topHaloReceived = false
	w.bottomHaloReceived = false

	numWorkers := len(distributed.WorkerAddresses)
	if w.workerID > 0 {
		w.neighbors.top = distributed.WorkerAddresses[w.workerID-1]
	} else {
		w.neighbors.top = distributed.WorkerAddresses[numWorkers-1]
	}
	if w.workerID < numWorkers-1 {
		w.neighbors.bottom = distributed.WorkerAddresses[w.workerID+1]
	} else {
		w.neighbors.bottom = distributed.WorkerAddresses[0]
	}

	// Don't connect to neighbors immediately - wait until first ProcessTurn
	// This avoids connection failures if neighbors aren't initialized yet

	res.Success = true
	return nil
}

func (w *WorkerOps) connectToNeighbors() {
	w.neighborClients.mu.Lock()
	defer w.neighborClients.mu.Unlock()

	// Close existing connections if any
	if w.neighborClients.topClient != nil {
		w.neighborClients.topClient.Close()
	}
	if w.neighborClients.bottomClient != nil {
		w.neighborClients.bottomClient.Close()
	}

	// Connect to top neighbor
	topClient, err := rpc.Dial("tcp", w.neighbors.top)
	if err != nil {
		log.Printf("Warning: Failed to connect to top neighbor %s: %v", w.neighbors.top, err)
	} else {
		w.neighborClients.topClient = topClient
		log.Printf("Connected to top neighbor %s", w.neighbors.top)
	}

	// Connect to bottom neighbor
	bottomClient, err := rpc.Dial("tcp", w.neighbors.bottom)
	if err != nil {
		log.Printf("Warning: Failed to connect to bottom neighbor %s: %v", w.neighbors.bottom, err)
	} else {
		w.neighborClients.bottomClient = bottomClient
		log.Printf("Connected to bottom neighbor %s", w.neighbors.bottom)
	}
}

func (w *WorkerOps) reconnectNeighbor(isTop bool) {
	w.neighborClients.mu.Lock()
	defer w.neighborClients.mu.Unlock()

	if isTop {
		if w.neighborClients.topClient != nil {
			w.neighborClients.topClient.Close()
			w.neighborClients.topClient = nil
		}

		client, err := rpc.Dial("tcp", w.neighbors.top)
		if err != nil {
			log.Printf("Failed to reconnect to top neighbor %s: %v", w.neighbors.top, err)
		} else {
			w.neighborClients.topClient = client
			log.Printf("Reconnected to top neighbor %s", w.neighbors.top)
		}
	} else {
		if w.neighborClients.bottomClient != nil {
			w.neighborClients.bottomClient.Close()
			w.neighborClients.bottomClient = nil
		}

		client, err := rpc.Dial("tcp", w.neighbors.bottom)
		if err != nil {
			log.Printf("Failed to reconnect to bottom neighbor %s: %v", w.neighbors.bottom, err)
		} else {
			w.neighborClients.bottomClient = client
			log.Printf("Reconnected to bottom neighbor %s", w.neighbors.bottom)
		}
	}
}

func (w *WorkerOps) ReceiveHalo(req distributed.HaloRequest, res *distributed.HaloResponse) error {
	w.mu.Lock()
	if req.IsTop {
		copy(w.topHalo, req.Row)
		log.Printf("Worker %d: received top halo for turn %d, len=%d, non-zero=%d",
			w.workerID, req.TurnNum, len(req.Row), countNonZero(req.Row))
	} else {
		copy(w.bottomHalo, req.Row)
		log.Printf("Worker %d: received bottom halo for turn %d, len=%d, non-zero=%d",
			w.workerID, req.TurnNum, len(req.Row), countNonZero(req.Row))
	}
	w.mu.Unlock()

	// Signal that a halo has been received for the current turn
	// Only set flag if this halo is for the current or a future turn
	w.haloMu.Lock()
	if req.TurnNum >= w.haloTurn {
		if req.IsTop {
			w.topHaloReceived = true
		} else {
			w.bottomHaloReceived = true
		}
		w.haloCond.Broadcast()
	}
	w.haloMu.Unlock()

	res.Success = true
	return nil
}

func countNonZero(row []byte) int {
	count := 0
	for _, b := range row {
		if b != 0 {
			count++
		}
	}
	return count
}

func (w *WorkerOps) GetHaloRow(req distributed.GetHaloRowRequest, res *distributed.GetHaloRowResponse) error {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if len(w.world) == 0 {
		return fmt.Errorf("worker not initialized")
	}

	res.Row = make([]byte, w.imageWidth)
	if req.IsTop {
		copy(res.Row, w.world[0])
	} else {
		copy(res.Row, w.world[len(w.world)-1])
	}

	return nil
}

func mod(x, m int) int {
	return (x + m) % m
}

func (w *WorkerOps) countNeighbours(x, y int) int {
	neighbours := 0
	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if i != 0 || j != 0 {
				newX := mod(x+j, w.imageWidth)
				newY := y + i
				var cell byte
				if newY < 0 {
					cell = w.topHalo[newX]
				} else if newY >= len(w.world) {
					cell = w.bottomHalo[newX]
				} else {
					cell = w.world[newY][newX]
				}
				if cell == 255 {
					neighbours++
				}
			}
		}
	}
	return neighbours
}

func (w *WorkerOps) processSection(startRow, endRow int, newWorld [][]byte, flipped *[]util.Cell, mu *sync.Mutex) {
	for y := startRow; y < endRow; y++ {
		for x := 0; x < w.imageWidth; x++ {
			neighbours := w.countNeighbours(x, y)
			currentCell := w.world[y][x]
			var newCell byte

			if currentCell == 255 {
				if neighbours == 2 || neighbours == 3 {
					newCell = 255
				} else {
					newCell = 0
				}
			} else {
				if neighbours == 3 {
					newCell = 255
				} else {
					newCell = 0
				}
			}

			newWorld[y][x] = newCell

			if currentCell != newCell {
				mu.Lock()
				*flipped = append(*flipped, util.Cell{X: x, Y: w.startY + y})
				mu.Unlock()
			}
		}
	}
}

func (w *WorkerOps) ProcessTurn(req distributed.ProcessTurnRequest, res *distributed.ProcessTurnResponse) error {
	// Ensure neighbor connections exist on first turn
	w.neighborClients.mu.RLock()
	needsConnection := w.neighborClients.topClient == nil || w.neighborClients.bottomClient == nil
	w.neighborClients.mu.RUnlock()

	if needsConnection {
		w.connectToNeighbors()
	}

	w.mu.Lock()

	if len(w.world) == 0 {
		w.mu.Unlock()
		return fmt.Errorf("worker not initialized")
	}

	// Copy our border rows to send to neighbors
	topRow := make([]byte, w.imageWidth)
	bottomRow := make([]byte, w.imageWidth)
	copy(topRow, w.world[0])
	copy(bottomRow, w.world[len(w.world)-1])

	w.mu.Unlock()

	// Reset halo receive flags for this turn
	w.haloMu.Lock()
	w.haloTurn = req.TurnNum
	w.topHaloReceived = false
	w.bottomHaloReceived = false
	w.haloMu.Unlock()

	// Push-based halo exchange: send our border rows to neighbors
	var wg sync.WaitGroup
	wg.Add(2)

	// Send our bottom row to bottom neighbor (becomes their top halo)
	go func() {
		defer wg.Done()
		w.neighborClients.mu.RLock()
		client := w.neighborClients.bottomClient
		w.neighborClients.mu.RUnlock()

		if client == nil {
			w.reconnectNeighbor(false)
			w.neighborClients.mu.RLock()
			client = w.neighborClients.bottomClient
			w.neighborClients.mu.RUnlock()
		}

		if client != nil {
			haloReq := distributed.HaloRequest{Row: bottomRow, IsTop: true, TurnNum: req.TurnNum}
			var haloRes distributed.HaloResponse
			err := client.Call("WorkerOps.ReceiveHalo", haloReq, &haloRes)
			if err != nil {
				log.Printf("Failed to send halo to bottom neighbor: %v", err)
				w.reconnectNeighbor(false)
			}
		}
	}()

	// Send our top row to top neighbor (becomes their bottom halo)
	go func() {
		defer wg.Done()
		w.neighborClients.mu.RLock()
		client := w.neighborClients.topClient
		w.neighborClients.mu.RUnlock()

		if client == nil {
			w.reconnectNeighbor(true)
			w.neighborClients.mu.RLock()
			client = w.neighborClients.topClient
			w.neighborClients.mu.RUnlock()
		}

		if client != nil {
			haloReq := distributed.HaloRequest{Row: topRow, IsTop: false, TurnNum: req.TurnNum}
			var haloRes distributed.HaloResponse
			err := client.Call("WorkerOps.ReceiveHalo", haloReq, &haloRes)
			if err != nil {
				log.Printf("Failed to send halo to top neighbor: %v", err)
				w.reconnectNeighbor(true)
			}
		}
	}()

	// Wait for our sends to complete
	wg.Wait()

	// Wait until we've received both halos from our neighbors
	w.haloMu.Lock()
	for !w.topHaloReceived || !w.bottomHaloReceived {
		w.haloCond.Wait()
	}
	w.haloMu.Unlock()

	w.mu.Lock()
	defer w.mu.Unlock()

	log.Printf("Worker %d: turn %d, starting computation. topHalo non-zero=%d, bottomHalo non-zero=%d, world rows=%d",
		w.workerID, req.TurnNum, countNonZero(w.topHalo), countNonZero(w.bottomHalo), len(w.world))

	// Use nextWorld as the write buffer (double-buffering)
	var flipped []util.Cell
	var flippedMu sync.Mutex

	if w.threads > 1 {
		rowsPerThread := len(w.world) / w.threads
		var wg2 sync.WaitGroup
		for i := 0; i < w.threads; i++ {
			start := i * rowsPerThread
			end := (i + 1) * rowsPerThread
			if i == w.threads-1 {
				end = len(w.world)
			}
			wg2.Add(1)
			go func(s, e int) {
				defer wg2.Done()
				w.processSection(s, e, w.nextWorld, &flipped, &flippedMu)
			}(start, end)
		}
		wg2.Wait()
	} else {
		w.processSection(0, len(w.world), w.nextWorld, &flipped, &flippedMu)
	}

	// Swap world and nextWorld (double-buffer swap)
	w.world, w.nextWorld = w.nextWorld, w.world
	w.currentTurn = req.TurnNum

	res.Flipped = flipped
	return nil
}

func (w *WorkerOps) GetState(req distributed.GetStateRequest, res *distributed.GetStateResponse) error {
	w.mu.RLock()
	defer w.mu.RUnlock()

	res.Section = make([][]byte, len(w.world))
	for i := range w.world {
		res.Section[i] = make([]byte, len(w.world[i]))
		copy(res.Section[i], w.world[i])
	}

	return nil
}

func (w *WorkerOps) Shutdown(req struct{}, res *struct{}) error {
	w.neighborClients.mu.Lock()
	defer w.neighborClients.mu.Unlock()

	if w.neighborClients.topClient != nil {
		w.neighborClients.topClient.Close()
		w.neighborClients.topClient = nil
	}
	if w.neighborClients.bottomClient != nil {
		w.neighborClients.bottomClient.Close()
		w.neighborClients.bottomClient = nil
	}

	log.Println("Worker shutting down, closed neighbor connections")
	return nil
}

func main() {
	port := flag.Int("port", 8031, "Port to listen on")
	flag.Parse()

	worker := new(WorkerOps)
	err := rpc.Register(worker)
	if err != nil {
		log.Fatal("Failed to register worker:", err)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal("Listen error:", err)
	}

	log.Printf("Worker listening on port %d\n", *port)
	rpc.Accept(listener)
}

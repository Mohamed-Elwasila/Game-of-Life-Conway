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
	world       [][]byte
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

	w.topHalo = make([]byte, w.imageWidth)
	w.bottomHalo = make([]byte, w.imageWidth)

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
	defer w.mu.Unlock()

	if req.IsTop {
		copy(w.topHalo, req.Row)
	} else {
		copy(w.bottomHalo, req.Row)
	}

	res.Success = true
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

	topRow := make([]byte, w.imageWidth)
	bottomRow := make([]byte, w.imageWidth)
	copy(topRow, w.world[0])
	copy(bottomRow, w.world[len(w.world)-1])

	w.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)

	// Send bottom row to bottom neighbor using persistent connection
	go func() {
		defer wg.Done()
		w.neighborClients.mu.RLock()
		client := w.neighborClients.bottomClient
		w.neighborClients.mu.RUnlock()

		if client != nil {
			haloReq := distributed.HaloRequest{Row: bottomRow, IsTop: true, TurnNum: req.TurnNum}
			var haloRes distributed.HaloResponse
			err := client.Call("WorkerOps.ReceiveHalo", haloReq, &haloRes)
			if err != nil {
				log.Printf("Failed to send halo to bottom neighbor, reconnecting: %v", err)
				w.reconnectNeighbor(false) // false = bottom
			}
		} else {
			w.reconnectNeighbor(false)
		}
	}()

	// Send top row to top neighbor using persistent connection
	go func() {
		defer wg.Done()
		w.neighborClients.mu.RLock()
		client := w.neighborClients.topClient
		w.neighborClients.mu.RUnlock()

		if client != nil {
			haloReq := distributed.HaloRequest{Row: topRow, IsTop: false, TurnNum: req.TurnNum}
			var haloRes distributed.HaloResponse
			err := client.Call("WorkerOps.ReceiveHalo", haloReq, &haloRes)
			if err != nil {
				log.Printf("Failed to send halo to top neighbor, reconnecting: %v", err)
				w.reconnectNeighbor(true) // true = top
			}
		} else {
			w.reconnectNeighbor(true)
		}
	}()

	wg.Wait()

	w.mu.Lock()
	defer w.mu.Unlock()

	newWorld := make([][]byte, len(w.world))
	for i := range newWorld {
		newWorld[i] = make([]byte, w.imageWidth)
	}

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
				w.processSection(s, e, newWorld, &flipped, &flippedMu)
			}(start, end)
		}
		wg2.Wait()
	} else {
		w.processSection(0, len(w.world), newWorld, &flipped, &flippedMu)
	}

	w.world = newWorld
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

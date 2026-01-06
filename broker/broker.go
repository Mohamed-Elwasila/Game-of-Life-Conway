package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"sync"
	"time"

	"uk.ac.bris.cs/gameoflife/distributed"
	"uk.ac.bris.cs/gameoflife/util"
)

// callWithTimeout wraps an RPC call with a timeout
func callWithTimeout(client *rpc.Client, method string, args interface{}, reply interface{}, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- client.Call(method, args, reply)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("RPC call %s timed out after %v", method, timeout)
	}
}

type BrokerOps struct {
	mu            sync.RWMutex
	world         [][]byte
	imageWidth    int
	imageHeight   int
	currentTurn   int
	isInitialized bool
	backupAddrs   []string
	isPrimary     bool
	workerClients map[int]*rpc.Client

	// Leader election fields
	brokerID      string
	currentTerm   int
	isLeader      bool
	lastHeartbeat time.Time
	peerBrokers   []string
}

func (b *BrokerOps) Init(req distributed.BrokerInitRequest, res *distributed.BrokerInitResponse) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.world = req.World
	b.imageWidth = req.ImageWidth
	b.imageHeight = req.ImageHeight
	b.currentTurn = 0
	b.isInitialized = true
	b.workerClients = make(map[int]*rpc.Client)

	numWorkers := len(distributed.WorkerAddresses)
	rowsPerWorker := req.ImageHeight / numWorkers

	for i := 0; i < numWorkers; i++ {
		startY := i * rowsPerWorker
		endY := startY + rowsPerWorker
		if i == numWorkers-1 {
			endY = req.ImageHeight
		}

		section := b.world[startY:endY]
		client, err := rpc.Dial("tcp", distributed.WorkerAddresses[i])
		if err != nil {
			log.Printf("Failed to connect to worker %d: %v", i, err)
			continue
		}

		initReq := distributed.InitRequest{
			World:       section,
			ImageWidth:  req.ImageWidth,
			ImageHeight: req.ImageHeight,
			StartY:      startY,
			EndY:        endY,
			WorkerID:    i,
			Threads:     req.Threads,
		}
		var initRes distributed.InitResponse
		// Use 10 second timeout for worker initialization
		err = callWithTimeout(client, "WorkerOps.Init", initReq, &initRes, 10*time.Second)
		if err != nil {
			log.Printf("Failed to initialize worker %d: %v", i, err)
			client.Close()
			continue
		}

		b.workerClients[i] = client
	}

	res.Success = true
	return nil
}

func (b *BrokerOps) ProcessTurn(req distributed.BrokerTurnRequest, res *distributed.BrokerTurnResponse) error {
	b.mu.RLock()
	if !b.isInitialized {
		b.mu.RUnlock()
		return fmt.Errorf("broker not initialized")
	}
	b.mu.RUnlock()

	type workerResult struct {
		workerID int
		flipped  []util.Cell
	}

	results := make(chan workerResult, len(b.workerClients))
	var wg sync.WaitGroup

	for workerID, client := range b.workerClients {
		wg.Add(1)
		go func(id int, c *rpc.Client) {
			defer wg.Done()

			turnReq := distributed.ProcessTurnRequest{TurnNum: req.TurnNum}
			var turnRes distributed.ProcessTurnResponse

			// Use 5 second timeout for turn processing
			err := callWithTimeout(c, "WorkerOps.ProcessTurn", turnReq, &turnRes, 5*time.Second)
			if err != nil {
				log.Printf("Worker %d failed: %v, attempting recovery", id, err)
				b.recoverWorker(id)
				return
			}

			results <- workerResult{workerID: id, flipped: turnRes.Flipped}
		}(workerID, client)
	}

	wg.Wait()
	close(results)

	var allFlipped []util.Cell
	for result := range results {
		allFlipped = append(allFlipped, result.flipped...)
	}

	b.mu.Lock()
	b.currentTurn = req.TurnNum
	b.mu.Unlock()

	res.Flipped = allFlipped
	return nil
}

func (b *BrokerOps) recoverWorker(workerID int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if client, ok := b.workerClients[workerID]; ok {
		client.Close()
		delete(b.workerClients, workerID)
	}

	numWorkers := len(distributed.WorkerAddresses)
	rowsPerWorker := b.imageHeight / numWorkers
	startY := workerID * rowsPerWorker
	endY := startY + rowsPerWorker
	if workerID == numWorkers-1 {
		endY = b.imageHeight
	}

	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		time.Sleep(time.Millisecond * 500)

		client, err := rpc.Dial("tcp", distributed.WorkerAddresses[workerID])
		if err != nil {
			log.Printf("Retry %d: Failed to reconnect to worker %d: %v", i+1, workerID, err)
			continue
		}

		// Create empty section for worker recovery
		// Worker will resync from neighbors via halo exchange
		section := make([][]byte, rowsPerWorker)
		for j := range section {
			section[j] = make([]byte, b.imageWidth)
		}

		initReq := distributed.InitRequest{
			World:       section,
			ImageWidth:  b.imageWidth,
			ImageHeight: b.imageHeight,
			StartY:      startY,
			EndY:        endY,
			WorkerID:    workerID,
			Threads:     4,
		}
		var initRes distributed.InitResponse
		err = client.Call("WorkerOps.Init", initReq, &initRes)
		if err != nil {
			log.Printf("Retry %d: Failed to reinitialize worker %d: %v", i+1, workerID, err)
			client.Close()
			continue
		}

		b.workerClients[workerID] = client
		log.Printf("Successfully recovered worker %d", workerID)
		return
	}

	log.Printf("Failed to recover worker %d after %d retries", workerID, maxRetries)
}

func (b *BrokerOps) GetState(req distributed.BrokerStateRequest, res *distributed.BrokerStateResponse) error {
	b.mu.RLock()
	if !b.isInitialized {
		b.mu.RUnlock()
		return fmt.Errorf("broker not initialized")
	}

	turn := b.currentTurn
	imageHeight := b.imageHeight
	imageWidth := b.imageWidth
	numWorkers := len(distributed.WorkerAddresses)
	rowsPerWorker := imageHeight / numWorkers
	b.mu.RUnlock()

	// Allocate full world state
	world := make([][]byte, imageHeight)
	for i := range world {
		world[i] = make([]byte, imageWidth)
	}

	// Query workers in parallel to reconstruct world state
	var wg sync.WaitGroup
	var mu sync.Mutex

	for workerID, client := range b.workerClients {
		wg.Add(1)
		go func(id int, c *rpc.Client) {
			defer wg.Done()

			var stateRes distributed.GetStateResponse
			// Use 3 second timeout for state retrieval
			err := callWithTimeout(c, "WorkerOps.GetState", distributed.GetStateRequest{}, &stateRes, 3*time.Second)
			if err != nil {
				log.Printf("Failed to get state from worker %d: %v", id, err)
				return
			}

			mu.Lock()
			startY := id * rowsPerWorker
			for i, row := range stateRes.Section {
				if startY+i < len(world) {
					copy(world[startY+i], row)
				}
			}
			mu.Unlock()
		}(workerID, client)
	}

	wg.Wait()

	res.World = world
	res.Turn = turn
	return nil
}

func (b *BrokerOps) GetAlive(req distributed.BrokerAliveRequest, res *distributed.BrokerAliveResponse) error {
	b.mu.RLock()
	if !b.isInitialized {
		b.mu.RUnlock()
		return fmt.Errorf("broker not initialized")
	}
	b.mu.RUnlock()

	// Query workers in parallel to count alive cells
	type workerAlive struct {
		cells []util.Cell
	}

	results := make(chan workerAlive, len(b.workerClients))
	var wg sync.WaitGroup

	for workerID, client := range b.workerClients {
		wg.Add(1)
		go func(id int, c *rpc.Client) {
			defer wg.Done()

			var stateRes distributed.GetStateResponse
			// Use 2 second timeout for alive cell counting
			err := callWithTimeout(c, "WorkerOps.GetState", distributed.GetStateRequest{}, &stateRes, 2*time.Second)
			if err != nil {
				log.Printf("Failed to get state from worker %d for alive count: %v", id, err)
				return
			}

			// Count alive cells in this worker's section
			var alive []util.Cell
			numWorkers := len(distributed.WorkerAddresses)
			rowsPerWorker := b.imageHeight / numWorkers
			startY := id * rowsPerWorker

			for y, row := range stateRes.Section {
				for x, cell := range row {
					if cell == 255 {
						alive = append(alive, util.Cell{X: x, Y: startY + y})
					}
				}
			}

			results <- workerAlive{cells: alive}
		}(workerID, client)
	}

	wg.Wait()
	close(results)

	var allAlive []util.Cell
	for result := range results {
		allAlive = append(allAlive, result.cells...)
	}

	res.Count = len(allAlive)
	res.Cells = allAlive
	return nil
}

func (b *BrokerOps) Shutdown(req distributed.BrokerShutdownRequest, res *distributed.BrokerShutdownResponse) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, client := range b.workerClients {
		var shutdownRes struct{}
		client.Call("WorkerOps.Shutdown", struct{}{}, &shutdownRes)
		client.Close()
	}

	res.Success = true
	return nil
}

func (b *BrokerOps) Health(req distributed.BrokerHealthRequest, res *distributed.BrokerHealthResponse) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	res.Healthy = b.isInitialized && len(b.workerClients) > 0 && b.isLeader
	res.Turn = b.currentTurn
	return nil
}

func (b *BrokerOps) Sync(req distributed.BrokerSyncRequest, res *distributed.BrokerSyncResponse) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Note: We no longer sync world state, only metadata
	// World state remains distributed across workers
	b.currentTurn = req.Turn
	b.isInitialized = req.World != nil
	res.Success = true
	return nil
}

func (b *BrokerOps) Heartbeat(req distributed.BrokerHeartbeatRequest, res *distributed.BrokerHeartbeatResponse) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Update last heartbeat time
	b.lastHeartbeat = time.Now()

	// If incoming term is higher, step down as leader
	if req.Term > b.currentTerm {
		b.currentTerm = req.Term
		b.isLeader = false
		log.Printf("Stepping down: received higher term %d from %s", req.Term, req.BrokerID)
	}

	// If sender claims to be leader with equal or higher term, accept it
	if req.IsLeader && req.Term >= b.currentTerm {
		b.isLeader = false
		b.currentTerm = req.Term
	}

	res.Success = true
	res.Term = b.currentTerm
	res.IsLeader = b.isLeader
	return nil
}

func (b *BrokerOps) runLeaderElection() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	electionTimeout := 3 * time.Second
	heartbeatInterval := 500 * time.Millisecond
	lastHeartbeatSent := time.Now()

	for range ticker.C {
		b.mu.Lock()
		timeSinceLastHeartbeat := time.Since(b.lastHeartbeat)
		isLeader := b.isLeader
		currentTerm := b.currentTerm
		brokerID := b.brokerID
		turn := b.currentTurn
		b.mu.Unlock()

		// If we haven't heard from leader in election timeout, start election
		if !isLeader && timeSinceLastHeartbeat > electionTimeout {
			b.mu.Lock()
			b.currentTerm++
			b.isLeader = true
			b.lastHeartbeat = time.Now()
			newTerm := b.currentTerm
			b.mu.Unlock()

			log.Printf("No leader heartbeat for %v, starting election (term %d)", electionTimeout, newTerm)
		}

		// If we are leader, send heartbeats to peers
		if isLeader && time.Since(lastHeartbeatSent) > heartbeatInterval {
			for _, peerAddr := range b.peerBrokers {
				go func(addr string) {
					client, err := rpc.Dial("tcp", addr)
					if err != nil {
						return
					}
					defer client.Close()

					hbReq := distributed.BrokerHeartbeatRequest{
						BrokerID: brokerID,
						Term:     currentTerm,
						IsLeader: true,
						LastTurn: turn,
					}
					var hbRes distributed.BrokerHeartbeatResponse
					// Use 1 second timeout for heartbeats
					err = callWithTimeout(client, "BrokerOps.Heartbeat", hbReq, &hbRes, 1*time.Second)
					if err == nil && hbRes.Term > currentTerm {
						b.mu.Lock()
						b.currentTerm = hbRes.Term
						b.isLeader = false
						b.mu.Unlock()
						log.Printf("Discovered higher term %d, stepping down", hbRes.Term)
					}
				}(peerAddr)
			}
			lastHeartbeatSent = time.Now()
		}
	}
}

func main() {
	port := flag.String("port", "8030", "Port to listen on")
	isPrimary := flag.Bool("primary", true, "Is this the primary broker")
	brokerID := flag.String("id", "", "Unique broker ID (defaults to port)")
	flag.Parse()

	// Default broker ID to port if not specified
	if *brokerID == "" {
		*brokerID = *port
	}

	// Build list of peer brokers (all brokers except this one)
	addr := "127.0.0.1:" + *port
	allBrokers := []string{distributed.BrokerAddress}
	allBrokers = append(allBrokers, distributed.BackupBrokerAddresses...)

	var peerBrokers []string
	for _, broker := range allBrokers {
		if broker != addr {
			peerBrokers = append(peerBrokers, broker)
		}
	}

	broker := &BrokerOps{
		isPrimary:     *isPrimary,
		backupAddrs:   distributed.BackupBrokerAddresses,
		brokerID:      *brokerID,
		currentTerm:   0,
		isLeader:      *isPrimary, // Primary starts as leader
		lastHeartbeat: time.Now(),
		peerBrokers:   peerBrokers,
	}
	rpc.Register(broker)

	// Start leader election goroutine
	go broker.runLeaderElection()

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal("Listen error:", err)
	}

	log.Printf("Broker %s listening on %s (initial leader: %v)", *brokerID, addr, *isPrimary)
	log.Printf("Peer brokers: %v", peerBrokers)
	rpc.Accept(listener)
}

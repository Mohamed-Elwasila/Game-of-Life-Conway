package gol

import (
	"fmt"
	"net/rpc"
	"sync"
	"time"

	"uk.ac.bris.cs/gameoflife/distributed"
	"uk.ac.bris.cs/gameoflife/util"
)

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioOutput   chan<- uint8
	ioInput    <-chan uint8
	keyPresses <-chan rune
}

func readPgmImage(p Params, c distributorChannels) [][]byte {
	c.ioCommand <- ioInput
	c.ioFilename <- fmt.Sprintf("%dx%d", p.ImageWidth, p.ImageHeight)

	world := make([][]byte, p.ImageHeight)
	for i := range world {
		world[i] = make([]byte, p.ImageWidth)
	}

	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			world[y][x] = <-c.ioInput
		}
	}

	return world
}

func countNeighbours(world [][]byte, x, y, width, height int) int {
	count := 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx := (x + dx + width) % width
			ny := (y + dy + height) % height
			if world[ny][nx] == 255 {
				count++
			}
		}
	}
	return count
}

type workerResult struct {
	startY  int
	data    [][]byte
	flipped []util.Cell
}

func worker(startY, endY int, p Params, world [][]byte, out chan<- workerResult) {
	result := make([][]byte, endY-startY)
	for i := range result {
		result[i] = make([]byte, p.ImageWidth)
	}
	var flipped []util.Cell

	for y := startY; y < endY; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			neighbours := countNeighbours(world, x, y, p.ImageWidth, p.ImageHeight)
			var newState byte
			if world[y][x] == 255 {
				if neighbours == 2 || neighbours == 3 {
					newState = 255
				} else {
					newState = 0
				}
			} else {
				if neighbours == 3 {
					newState = 255
				} else {
					newState = 0
				}
			}
			result[y-startY][x] = newState
			if newState != world[y][x] {
				flipped = append(flipped, util.Cell{X: x, Y: y})
			}
		}
	}

	out <- workerResult{startY: startY, data: result, flipped: flipped}
}

func calculateNextState(p Params, world [][]byte, c distributorChannels, turn int) [][]byte {
	newWorld := make([][]byte, p.ImageHeight)
	for i := range newWorld {
		newWorld[i] = make([]byte, p.ImageWidth)
	}

	out := make(chan workerResult)
	rowsPerWorker := p.ImageHeight / p.Threads

	for i := 0; i < p.Threads; i++ {
		startY := i * rowsPerWorker
		endY := startY + rowsPerWorker
		if i == p.Threads-1 {
			endY = p.ImageHeight
		}
		go worker(startY, endY, p, world, out)
	}

	for i := 0; i < p.Threads; i++ {
		result := <-out
		for j, row := range result.data {
			newWorld[result.startY+j] = row
		}
		if len(result.flipped) > 0 {
			c.events <- CellsFlipped{CompletedTurns: turn, Cells: result.flipped}
		}
	}

	return newWorld
}

func getAliveCells(p Params, world [][]byte) []util.Cell {
	var alive []util.Cell
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			if world[y][x] == 255 {
				alive = append(alive, util.Cell{X: x, Y: y})
			}
		}
	}
	return alive
}

func savePgm(p Params, c distributorChannels, world [][]byte, turn int) {
	c.ioCommand <- ioOutput
	c.ioFilename <- fmt.Sprintf("%dx%dx%d", p.ImageWidth, p.ImageHeight, turn)
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			c.ioOutput <- world[y][x]
		}
	}
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	c.events <- ImageOutputComplete{CompletedTurns: turn, Filename: fmt.Sprintf("%dx%dx%d", p.ImageWidth, p.ImageHeight, turn)}
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels) {

	world := readPgmImage(p, c)

	turn := 0

	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			if world[y][x] == 255 {
				c.events <- CellFlipped{CompletedTurns: turn, Cell: util.Cell{X: x, Y: y}}
			}
		}
	}

	c.events <- StateChange{turn, Executing}

	var client *rpc.Client
	var err error

	brokerAddrs := []string{distributed.BrokerAddress}
	brokerAddrs = append(brokerAddrs, distributed.BackupBrokerAddresses...)

	for _, addr := range brokerAddrs {
		client, err = rpc.Dial("tcp", addr)
		if err == nil {
			break
		}
	}
	if client == nil {
		panic("Failed to connect to any broker")
	}
	defer client.Close()

	initReq := distributed.BrokerInitRequest{
		World:       world,
		ImageWidth:  p.ImageWidth,
		ImageHeight: p.ImageHeight,
		Threads:     p.Threads,
	}
	var initRes distributed.BrokerInitResponse
	err = client.Call("BrokerOps.Init", initReq, &initRes)
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize broker: %v", err))
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	done := make(chan bool)
	var mu sync.RWMutex
	paused := false
	quitting := false

	go func() {
		for {
			select {
			case <-ticker.C:
				var aliveRes distributed.BrokerAliveResponse
				err := client.Call("BrokerOps.GetAlive", distributed.BrokerAliveRequest{}, &aliveRes)
				if err == nil {
					mu.RLock()
					currentTurn := turn
					mu.RUnlock()
					c.events <- AliveCellsCount{CompletedTurns: currentTurn, CellsCount: aliveRes.Count}
				}
			case <-done:
				return
			}
		}
	}()

	for t := 0; t < p.Turns; t++ {
		select {
		case key := <-c.keyPresses:
			switch key {
			case 's':
				mu.RLock()
				currentTurn := turn
				mu.RUnlock()
				var stateRes distributed.BrokerStateResponse
				err := client.Call("BrokerOps.GetState", distributed.BrokerStateRequest{}, &stateRes)
				if err == nil {
					savePgm(p, c, stateRes.World, currentTurn)
				}
			case 'p':
				mu.Lock()
				paused = !paused
				currentTurn := turn
				if paused {
					c.events <- StateChange{currentTurn, Paused}
				} else {
					c.events <- StateChange{currentTurn, Executing}
				}
				mu.Unlock()
				for paused && !quitting {
					key := <-c.keyPresses
					if key == 'p' {
						mu.Lock()
						paused = false
						currentTurn := turn
						c.events <- StateChange{currentTurn, Executing}
						mu.Unlock()
					} else if key == 's' {
						mu.RLock()
						currentTurn := turn
						mu.RUnlock()
						var stateRes distributed.BrokerStateResponse
						err := client.Call("BrokerOps.GetState", distributed.BrokerStateRequest{}, &stateRes)
						if err == nil {
							savePgm(p, c, stateRes.World, currentTurn)
						}
					} else if key == 'q' {
						quitting = true
					}
				}
			case 'q':
				quitting = true
			}
			if quitting {
				break
			}
		default:
		}

		if quitting {
			break
		}

		turnReq := distributed.BrokerTurnRequest{TurnNum: t + 1}
		var turnRes distributed.BrokerTurnResponse
		err := client.Call("BrokerOps.ProcessTurn", turnReq, &turnRes)
		if err != nil {
			for i, addr := range brokerAddrs {
				if i == 0 {
					continue
				}
				newClient, err := rpc.Dial("tcp", addr)
				if err == nil {
					client.Close()
					client = newClient
					var healthRes distributed.BrokerHealthResponse
					err = client.Call("BrokerOps.Health", distributed.BrokerHealthRequest{}, &healthRes)
					if err == nil && healthRes.Healthy {
						break
					}
				}
			}
			continue
		}

		for _, cell := range turnRes.Flipped {
			c.events <- CellFlipped{CompletedTurns: t + 1, Cell: cell}
		}

		c.events <- TurnComplete{CompletedTurns: t + 1}

		// Update turn under lock for ticker goroutine to read
		mu.Lock()
		turn = t + 1
		mu.Unlock()
	}

	done <- true

	var stateRes distributed.BrokerStateResponse
	err = client.Call("BrokerOps.GetState", distributed.BrokerStateRequest{}, &stateRes)
	if err == nil {
		savePgm(p, c, stateRes.World, turn)
		alive := getAliveCells(p, stateRes.World)
		c.events <- FinalTurnComplete{CompletedTurns: turn, Alive: alive}
	}

	var shutdownRes distributed.BrokerShutdownResponse
	client.Call("BrokerOps.Shutdown", distributed.BrokerShutdownRequest{}, &shutdownRes)

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	c.events <- StateChange{turn, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

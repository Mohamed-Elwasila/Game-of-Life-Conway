package tests

import (
	"fmt"
	"testing"

	"uk.ac.bris.cs/gameoflife/gol"
)

// TestWorkerDistribution tests different thread configurations
func TestWorkerDistribution(t *testing.T) {
	sizes := []int{16, 64}
	threadCounts := []int{1, 2, 4, 8}

	for _, size := range sizes {
		for _, threads := range threadCounts {
			p := gol.Params{
				ImageWidth:  size,
				ImageHeight: size,
				Turns:       10,
				Threads:     threads,
			}

			t.Run(fmt.Sprintf("%dx%d", size, threads), func(t *testing.T) {
				events := make(chan gol.Event)
				go gol.Run(p, events, nil)

				completed := false
				for event := range events {
					switch event.(type) {
					case gol.FinalTurnComplete:
						completed = true
					}
				}

				if !completed {
					t.Error("Simulation did not complete")
				}
			})
		}
	}
}

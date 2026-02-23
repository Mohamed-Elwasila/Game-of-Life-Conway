package tests

import (
	"testing"

	"uk.ac.bris.cs/gameoflife/gol"
)

// TestCellCountTracking tests that alive cell counts are properly tracked
func TestCellCountTracking(t *testing.T) {
	p := gol.Params{
		ImageWidth:  16,
		ImageHeight: 16,
		Turns:       5,
		Threads:     4,
	}

	t.Run("CellCountEvents", func(t *testing.T) {
		events := make(chan gol.Event)
		go gol.Run(p, events, nil)

		receivedCount := false
		for event := range events {
			switch e := event.(type) {
			case gol.AliveCellsCount:
				receivedCount = true
				if e.CellsCount < 0 {
					t.Error("Cell count cannot be negative")
				}
			case gol.FinalTurnComplete:
				if !receivedCount {
					t.Error("No cell count events received")
				}
				return
			}
		}
	})
}

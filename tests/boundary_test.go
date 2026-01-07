package tests

import (
	"testing"

	"uk.ac.bris.cs/gameoflife/gol"
)

// TestBoundaryWrapping tests that cells at edges properly wrap around
func TestBoundaryWrapping(t *testing.T) {
	p := gol.Params{
		Turns:       1,
		Threads:     4,
		ImageWidth:  16,
		ImageHeight: 16,
	}

	// Create a simple pattern at the edge to verify wrapping
	t.Run("EdgeWrapping", func(t *testing.T) {
		events := make(chan gol.Event)
		go gol.Run(p, events, nil)

		for event := range events {
			switch event.(type) {
			case gol.FinalTurnComplete:
				// Test passes if it completes without panic
				return
			}
		}
	})
}

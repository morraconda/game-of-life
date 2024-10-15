package gol

import (
	"fmt"

	"uk.ac.bris.cs/gameoflife/util"
)

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioOutput   chan<- uint8
	ioInput    <-chan uint8
}

// returns list of cells adjacent to the one given, accounting for wraparounds
func getAdjacentCells(cell util.Cell, p Params) []util.Cell {
	result := make([]util.Cell, 8)
	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if i == 0 && j == 0 {
			} else {
				thing := util.Cell{X: cell.X + i%p.ImageWidth, Y: cell.Y + j%p.ImageHeight}
				result = append(result, thing)
			}
		}
	}
	return result
}

// return how many cells in a list are black
func countAdjacentCells(cell util.Cell, world [][]byte, p Params) int {
	count := 0
	cells := getAdjacentCells(util.Cell{X: cell.X, Y: cell.Y}, p)
	for _, cell := range cells {
		//count += world[cell.Y][cell.X] / 255
		if world[cell.Y][cell.X] == 255 {
			count++
		}
	}
	return count
}

// get next value of a cell, given a world
func getNextCell(cell util.Cell, world [][]byte, p Params) byte {
	neighbours := countAdjacentCells(cell, world, p)
	if neighbours == 3 {
		return 255
	} else if neighbours == 2 {
		return world[cell.Y][cell.X]
	} else {
		return 0
	}
}

// gets initial 2D slice from input
func getInitialWorld(input <-chan byte, p Params) [][]byte {
	// initialise 2D slice of rows
	world := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageHeight; i++ {
		// initialise row, set the contents of the row accordingly
		world[i] = make([]byte, p.ImageWidth)
		for j := 0; j < p.ImageWidth; j++ {
			world[i][j] = <-input
		}
	}
	return world
}

// writes board state to output
func writeToOutput(world [][]byte, output chan<- byte, p Params) {}

// get next board state
// TODO: make this parallel
func simulateTurn(world [][]byte, p Params) [][]byte {
	// initialise 2D slice of rows
	newWorld := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageHeight; i++ {
		// initialise row, set the contents of the row accordingly
		newWorld[i] = make([]byte, p.ImageWidth)
		for j := 0; j < p.ImageWidth; j++ {
			newWorld[i][j] = getNextCell(util.Cell{X: i, Y: j}, world, p)
		}
	}
	return newWorld
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels) {
	fmt.Println("called distributor")
	world := getInitialWorld(c.ioInput, p)
	fmt.Println("hi")

	// execute all turns of the Game of Life.
	for l := 0; l < p.Turns; l++ {
		world = simulateTurn(world, p)
		//c.events <- StateChange{turn, Executing}
	}

	output := make([]util.Cell, p.ImageHeight*p.ImageWidth)
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			if world[i][j] == 255 {
				output = append(output, util.Cell{X: i, Y: j})
			}
		}
	}

	// TODO: Report the final state using FinalTurnCompleteEvent.
	c.events <- FinalTurnComplete{p.Turns, output}

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	c.events <- StateChange{p.Turns, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

package gol

import (
	"fmt"
	"strconv"

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

// prints cell contents
func printCell(cell util.Cell) {
	fmt.Printf("(%d, %d) ", cell.X, cell.Y)
}

func printCells(cells []util.Cell) {
	for _, cell := range cells {
		printCell(cell)
	}
}

// get filename from params
func getFilename(p Params) string {
	return strconv.Itoa(p.ImageWidth) + "x" + strconv.Itoa(p.ImageHeight)
}

// returns in-bounds version of a cell if out of bounds
func wrap(cell util.Cell, p Params) util.Cell {
	if cell.X == -1 {
		cell.X = p.ImageWidth - 1
	} else if cell.X == p.ImageHeight {
		cell.X = 0
	}

	if cell.Y == -1 {
		cell.Y = p.ImageHeight - 1
	} else if cell.Y == p.ImageHeight {
		cell.Y = 0
	}
	return cell
}

// returns list of cells adjacent to the one given, accounting for wraparounds
func getAdjacentCells(cell util.Cell, p Params) []util.Cell {
	var adjacent []util.Cell
	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if !(i == 0 && j == 0) {
				next := util.Cell{X: cell.X + j, Y: cell.Y + i}
				next = wrap(next, p)
				adjacent = append(adjacent, next)
			}
		}
	}
	return adjacent
}

// return how many cells in a list are black
func countAdjacentCells(current util.Cell, world [][]byte, p Params) int {
	count := 0
	cells := getAdjacentCells(current, p)
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
func writeToOutput(world [][]byte, p Params, c chan<- Event) {
	var output []util.Cell
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			if world[i][j] == 255 {
				output = append(output, util.Cell{X: j, Y: i})
			}
		}
	}
	//printCells(output)
	c <- FinalTurnComplete{p.Turns, output}
}

// get next board state and flipped cells
// TODO: make this parallel
func simulateTurn(world [][]byte, startY int, p Params, outWorld chan<- [][]byte, outFlipped chan<- []util.Cell) {
	// initialise 2D slice of rows
	newWorld := make([][]byte, p.ImageHeight)
	var flipped []util.Cell
	for i := startY; i < p.ImageHeight/p.Threads; i++ {
		// initialise row, set the contents of the row accordingly
		newWorld[i] = make([]byte, p.ImageWidth)
		for j := 0; j < p.ImageWidth; j++ {
			// check for flipped cells
			cell := util.Cell{X: j, Y: i}
			old := newWorld[i][j]
			next := getNextCell(cell, world, p)
			if old != next {
				flipped = append(flipped, cell)
			}
			newWorld[i][j] = next
		}
	}
	//printCells(flipped)
	outWorld <- newWorld
	outFlipped <- flipped
	return
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels) {
	c.ioCommand <- ioInput
	c.ioFilename <- getFilename(p)
	world := getInitialWorld(c.ioInput, p)

	// execute all turns of the Game of Life.
	for turn := 0; turn < p.Turns; turn++ {
		newWorld := make([][]byte, p.ImageHeight)
		for i := 0; i < p.ImageHeight; i++ {
			newWorld[i] = make([]byte, p.ImageWidth)
		}
		var flipped []util.Cell
		var startY = 0

		//Make return channels for workers
		worldChans := make([]chan [][]uint8, p.Threads)
		for i := 0; i < p.Threads; i++ {
			worldChans[i] = make(chan [][]uint8)
		}
		flippedChans := make([]chan []util.Cell, p.Threads)
		for i := 0; i < p.Threads; i++ {
			flippedChans[i] = make(chan []util.Cell)
		}

		//Dispatch workers
		incrementY := p.ImageHeight / p.Threads
		for i := 0; i < p.Threads; i++ {
			go simulateTurn(world, startY, p, worldChans[i], flippedChans[i])
			startY += incrementY
		}

		//Receive data from workers
		for i := 0; i < p.Threads; i++ {
			select {
			case worldData := <-worldChans[i]:
				copy(newWorld, worldData)
			case flippedData := <-flippedChans[i]:
				flipped = append(flipped, flippedData...)
			}
		}

		world = newWorld
		c.events <- CellsFlipped{turn, flipped}
		c.events <- StateChange{turn, Executing}
	}

	writeToOutput(world, p, c.events)

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	c.events <- StateChange{p.Turns, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

package gol

import (
	"fmt"
	"strconv"
	"uk.ac.bris.cs/gameoflife/gol/stubs"

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
func getFilename(p stubs.Params) string {
	return strconv.Itoa(p.ImageWidth) + "x" + strconv.Itoa(p.ImageHeight)
}

// gets initial 2D slice from input
func getInitialWorld(input <-chan byte, p stubs.Params) [][]byte {
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
func writeToOutput(world [][]byte, p stubs.Params, c chan<- Event) {
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

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p stubs.Params, c distributorChannels) {
	c.ioCommand <- ioInput
	c.ioFilename <- getFilename(p)
	world := getInitialWorld(c.ioInput, p)

	// execute all turns of the Game of Life.
	for turn := 0; turn < p.Turns; turn++ {
		var newWorld [][]byte
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
			if i == p.Threads-1 {
				go simulateTurn(world, startY, p.ImageHeight, p, worldChans[i], flippedChans[i])
			} else {
				go simulateTurn(world, startY, (p.ImageHeight/p.Threads)+startY, p, worldChans[i], flippedChans[i])
			}
			startY += incrementY
		}

		//Receive data from workers
		for i := 0; i < p.Threads; i++ {
			worldData := <-worldChans[i]
			newWorld = append(newWorld, worldData...)
			flippedData := <-flippedChans[i]
			flipped = append(flipped, flippedData...)
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

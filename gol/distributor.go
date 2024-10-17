package gol

import (
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
	"sync"
	"time"
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
func getInputFilename(p Params) string {
	return fmt.Sprintf("%dx%d", p.ImageWidth, p.ImageHeight)
}

func getOutputFilename(p Params) string {
	return fmt.Sprintf("%dx%dx%d", p.ImageWidth, p.ImageHeight, p.Turns)
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
func writeToOutput(world [][]byte, turn int, p Params,
	eventChan chan<- Event, outputChan chan<- byte) {
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			outputChan <- world[i][j]
		}
	}
	eventChan <- ImageOutputComplete{turn, getOutputFilename(p)}
}

// get list of alive cells from a world
func getAliveCells(world [][]byte) []util.Cell {
	var alive []util.Cell
	for i := 0; i < len(world); i++ {
		for j := 0; j < len(world[i]); j++ {
			if world[i][j] == 255 {
				alive = append(alive, util.Cell{X: j, Y: i})
			}
		}
	}
	return alive
}

// get next board state and flipped cells
func simulateTurn(world [][]byte, startY int, endY int, p Params,
	outWorld chan<- [][]byte, outFlipped chan<- []util.Cell) {
	// initialise 2D slice of rows
	newWorld := make([][]byte, endY-startY)
	var flipped []util.Cell
	for i := startY; i < endY; i++ {
		// initialise row, set the contents of the row accordingly
		newWorld[i-startY] = make([]byte, p.ImageWidth)
		for j := 0; j < p.ImageWidth; j++ {
			// check for flipped cells
			cell := util.Cell{X: j, Y: i}
			old := newWorld[i-startY][j]
			next := getNextCell(cell, world, p)
			if old != next {
				flipped = append(flipped, cell)
			}
			newWorld[i-startY][j] = next
		}
	}
	//printCells(flipped)
	outWorld <- newWorld
	outFlipped <- flipped
	return
}

// reports state every 2 seconds
func reportState(turn *int, world *[][]byte, c chan<- Event, done <-chan bool) {
	finished := false
	for !finished {
		select {
		case <-done:
			finished = true
		case <-time.After(2 * time.Second):
			alive := getAliveCells(*world)
			c <- Event(AliveCellsCount{*turn, len(alive)})
		}
	}
}

// TODO: handle user keypresses
func handleKeypress(turn *int, world *[][]byte, keypresses <-chan rune, eventChan chan<- Event, done <-chan bool) {
	finished := false
	for !finished {
		select {
		case <-done:
			finished = true
		case key := <-keypresses:
			switch key {
			case sdl.K_s:
				//alive := getAliveCells(*world)
				//eventChan <- ImageOutputComplete{turn, }
			case sdl.K_q:
			case sdl.K_p:
			}
		}
	}
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels) {
	// init crap
	c.ioCommand <- ioInput
	c.ioFilename <- getInputFilename(p)
	world := getInitialWorld(c.ioInput, p)
	turn := 0
	finished := make(chan bool)
	wg := sync.WaitGroup{}
	wg.Add(1)
	// start alive cells ticker
	go func() {
		reportState(&turn, &world, c.events, finished)
		defer wg.Done()
	}()

	// execute all turns of the Game of Life.
	for h := 0; h < p.Turns; h++ {
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
		turn++
		c.events <- CellsFlipped{turn, flipped}
		c.events <- StateChange{turn, Executing}
	}

	c.ioCommand <- ioOutput
	c.ioFilename <- getOutputFilename(p)
	writeToOutput(world, p.Turns, p, c.events, c.ioOutput)

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	c.events <- StateChange{p.Turns, Quitting}
	aliveCells := getAliveCells(world)
	c.events <- FinalTurnComplete{p.Turns, aliveCells}

	// close alive cells reporting
	finished <- true
	wg.Wait()
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

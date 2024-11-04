package gol

import (
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
	"sync"
	"time"
	"uk.ac.bris.cs/gameoflife/util"
)

var superMX sync.RWMutex
var wg sync.WaitGroup

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioOutput   chan<- uint8
	ioInput    <-chan uint8
}

// get filename from params
func getInputFilename(p Params) string {
	return fmt.Sprintf("%dx%d", p.ImageWidth, p.ImageHeight)
}

func getOutputFilename(p Params, t int) string {
	return fmt.Sprintf("%dx%dx%d", p.ImageWidth, p.ImageHeight, t)
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
func writeToOutput(world [][]byte, turn int, p Params, outputChan chan<- byte) {
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			outputChan <- world[i][j]
		}
	}
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

// reports state every 2 seconds
func reportState(turn *int, world *[][]byte, c chan<- Event, finished <-chan bool) {
	for {
		select {
		case <-finished:
			wg.Done()
			return
		case <-time.After(2 * time.Second):
			superMX.Lock()
			alive := getAliveCells(*world)
			c <- Event(AliveCellsCount{*turn, len(alive)})
			superMX.Unlock()
		}
	}
}

func saveOutput(world [][]byte, turn int, p Params, c distributorChannels) {
	// write to output
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	c.ioCommand <- ioOutput
	c.ioFilename <- getOutputFilename(p, turn)
	writeToOutput(world, turn, p, c.ioOutput)
	// close io
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// send write event
	c.events <- ImageOutputComplete{turn, getOutputFilename(p, turn)}
}

func handleKeypress(keypresses <-chan rune, turn int, world [][]byte, finished <-chan bool,
	quit chan<- bool, pause chan<- bool, p Params, c distributorChannels) {
	paused := false
	for {
		select {
		case <-finished: // stop handling
			wg.Done()
			return
		case key := <-keypresses:
			switch key {
			case sdl.K_s: // save
				if !paused {
					pause <- true
				}
				superMX.Lock()
				saveOutput(world, turn, p, c)
				superMX.Unlock()
				if !paused {
					pause <- false
				}
			case sdl.K_q: // quit
				if !paused {
					pause <- true
				}
				superMX.Lock()
				saveOutput(world, turn, p, c)
				c.events <- FinalTurnComplete{turn, getAliveCells(world)}
				c.events <- StateChange{turn, Quitting}
				superMX.Unlock()
				quit <- true
				pause <- false
			case sdl.K_k: // shutdown
				if !paused {
					pause <- true
				}
				superMX.Lock()
				saveOutput(world, turn, p, c)

				c.events <- FinalTurnComplete{turn, getAliveCells(world)}
				c.events <- StateChange{turn, Quitting}
				superMX.Unlock()
				quit <- true
				pause <- false
			case sdl.K_p: // pause
				if paused {
					paused = false
					fmt.Println("continuing")
					superMX.Lock()
					c.events <- StateChange{turn, Executing}
					superMX.Unlock()
					pause <- false
				} else {
					paused = true
					fmt.Println(turn)
					superMX.Lock()
					c.events <- StateChange{turn, Paused}
					superMX.Unlock()
					pause <- true
				}
			}
		}
	}
}

// distributor executes all turns, sends work to broker
func distributor(p Params, c distributorChannels, keypresses <-chan rune) {
	// Get initial world
	c.ioCommand <- ioInput
	c.ioFilename <- getInputFilename(p)
	world := getInitialWorld(c.ioInput, p)

	// Channels for communicating with ticker and keypress handler
	wg.Add(2)
	quit := make(chan bool, 1)
	finishedR := make(chan bool)
	finishedL := make(chan bool)
	pause := make(chan bool)
	turn := 0
	// Start alive cells ticker and keypress handler
	go handleKeypress(keypresses, turn, world, finishedL, quit, pause, p, c)
	go reportState(&turn, &world, c.events, finishedR)
	exit := false

	//get the initial alive cells and use them as input to CellsFlipped
	initialFlippedCells := getAliveCells(world)
	c.events <- CellsFlipped{turn, initialFlippedCells}
	c.events <- StateChange{0, Executing}
	// Main game loop
mainLoop:
	for turn < p.Turns {
		select {
		case paused := <-pause:
			// Halt loop and wait until pause is set to false
			for paused {
				paused = <-pause
			}
		case <-quit:
			// Exit
			exit = true
			break mainLoop
		default:
			// advance to next state
			newWorld, flipped := simulateTurn(world, p)
			superMX.Lock()
			world = newWorld
			saveOutput(world, turn, p, c)
			c.events <- CellsFlipped{turn, flipped}
			c.events <- TurnComplete{turn}
			turn++
			superMX.Unlock()

		}
	}
	// Signal goroutines to return
	finishedR <- true
	finishedL <- true
	// If we finished the turns output the result
	if !exit {
		superMX.Lock()
		saveOutput(world, turn, p, c)
		aliveCells := getAliveCells(world)
		c.events <- FinalTurnComplete{turn, aliveCells}
		c.events <- StateChange{turn, Quitting}
		superMX.Unlock()
	}

	// Wait for goroutines to confirm closure
	wg.Wait()
	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(finishedR)
	close(finishedL)
	close(pause)
	close(quit)
	close(c.events)
}

// Receive a board and slice it up into jobs
func simulateTurn(world [][]byte, p Params) ([][]byte, []util.Cell) {

	// initialise output channels
	outChans := make([]chan [][]byte, p.Threads)
	flippedChan := make(chan []util.Cell, p.Threads)
	var newWorld [][]byte
	var flipped []util.Cell

	// distribution logic
	incrementY := p.ImageHeight / p.Threads
	startY := 0
	for i := 0; i < p.Threads; i++ {
		outChans[i] = make(chan [][]byte)
		var endY int
		if i == p.Threads-1 {
			endY = p.ImageHeight
		} else {
			endY = incrementY + startY
		}
		go worker(world, outChans[i], flippedChan, startY, endY, p)
		startY += incrementY
	}

	// piece together new output
	for i := 0; i < p.Threads; i++ {
		result := <-outChans[i]
		close(outChans[i])
		newWorld = append(newWorld, result...)
		flipped = append(flipped, <-flippedChan...)
	}
	close(flippedChan)
	return newWorld, flipped
}

// returns in-bounds version of a cell if out of bounds
func wrap(cell util.Cell, width int, height int) util.Cell {
	if cell.X == -1 {
		cell.X = width - 1
	} else if cell.X == width {
		cell.X = 0
	}

	if cell.Y == -1 {
		cell.Y = height - 1
	} else if cell.Y == height {
		cell.Y = 0
	}
	return cell
}

// returns list of cells adjacent to the one given, accounting for wraparounds
func getAdjacentCells(cell util.Cell, width int, height int) []util.Cell {
	var adjacent []util.Cell
	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if !(i == 0 && j == 0) {
				next := util.Cell{X: cell.X + j, Y: cell.Y + i}
				next = wrap(next, width, height)
				adjacent = append(adjacent, next)
			}
		}
	}
	return adjacent
}

// return how many cells in a list are black
func countAdjacentCells(current util.Cell, world [][]byte, width int, height int) int {
	count := 0
	cells := getAdjacentCells(current, width, height)
	for _, cell := range cells {
		//count += world[cell.Y][cell.X] / 255
		if world[cell.Y][cell.X] == 255 {
			count++
		}
	}
	return count
}

// get next value of a cell, given a world
func getNextCell(cell util.Cell, world [][]byte, width int, height int) byte {
	neighbours := countAdjacentCells(cell, world, width, height)
	if neighbours == 3 {
		return 255
	} else if neighbours == 2 {
		return world[cell.Y][cell.X]
	} else {
		return 0
	}
}

type Compute struct{}

func worker(world [][]byte, outChan chan<- [][]byte, flippedChan chan<- []util.Cell,
	startY int, endY int, p Params) {
	// initialise 2D slice of rows
	width := p.ImageWidth
	height := p.ImageHeight

	newWorld := make([][]byte, endY-startY)
	var flipped []util.Cell
	for i := startY; i < endY; i++ {
		// initialise row, set the contents of the row accordingly
		newWorld[i-startY] = make([]byte, width)
		for j := 0; j < width; j++ {
			// check for flipped cells
			cell := util.Cell{X: j, Y: i}
			old := newWorld[i-startY][j]
			next := getNextCell(cell, world, width, height)
			if old != next {
				flipped = append(flipped, cell)
			}
			newWorld[i-startY][j] = next
		}
	}

	outChan <- newWorld
	flippedChan <- flipped
}

type Request struct {
	World  [][]byte
	StartY int
	EndY   int
	Height int
	Width  int
}

type Response struct {
	World   [][]byte
	Flipped []util.Cell
}

type Update struct {
	Flipped []util.Cell
	World   [][]byte
}

type Subscription struct {
	FactoryAddress string
	Callback       string
}

type StatusReport struct {
	Message string
}

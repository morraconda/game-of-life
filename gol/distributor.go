package gol

import (
	"fmt"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
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
func reportState(turn *int, world *[][]byte, c chan<- Event, done <-chan bool, lock *sync.Mutex) {
	for {
		select {
		case <-done:
			return
		case <-time.After(2 * time.Second):
			lock.Lock()
			alive := getAliveCells(*world)
			c <- Event(AliveCellsCount{*turn, len(alive)})
			lock.Unlock()
		}
	}
}

func saveOutput(world [][]byte, turn int, p Params, c distributorChannels) {
	// write to output
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	c.ioCommand <- ioOutput
	filename := getOutputFilename(p, turn)
	c.ioFilename <- filename

	writeToOutput(world, turn, p, c.ioOutput)
	fmt.Println("-- successfully wrote to output")
	// close io
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// send write event
	fmt.Println("-- sending image event")
	c.events <- ImageOutputComplete{turn, filename}
}

func handleKeypress(keypresses <-chan rune, turn *int, world *[][]byte, done <-chan bool,
	quit chan<- bool, pauseChan chan<- bool, p Params, c distributorChannels, lock *sync.Mutex) {
	paused := false
	for {
		select {
		case <-done:
			return
		case key := <-keypresses:
			switch key {
			case sdl.K_s: // save
				fmt.Println("-- s pressed")
				lock.Lock()
				saveOutput(*world, *turn, p, c)
				lock.Unlock()
			case sdl.K_q: // quit
				fmt.Println("-- q pressed")
				pauseChan <- false
				quit <- true
			case sdl.K_p: // pause
				fmt.Println("-- p pressed")
				lock.Lock()
				if paused {
					fmt.Println("-- continuing")
					c.events <- StateChange{*turn, Executing}
					pauseChan <- false
				} else {
					fmt.Println("-- paused on Turn", *turn)
					c.events <- StateChange{*turn, Paused}
					pauseChan <- true
				}
				lock.Unlock()
				paused = !paused
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
	pauseChan := make(chan bool, 1)
	finishedL := make(chan bool, 1)
	finishedR := make(chan bool, 1)
	quit := make(chan bool, 1)
	turn := 0
	var wg sync.WaitGroup
	var lock sync.Mutex

	// get the initial alive cells and use them as input to CellsFlipped
	initialFlippedCells := getAliveCells(world)
	c.events <- CellsFlipped{turn, initialFlippedCells}
	c.events <- StateChange{turn, Executing}

	// Start alive cells ticker and keypress handler
	wg.Add(2)
	go func() {
		handleKeypress(keypresses, &turn, &world, finishedL, quit, pauseChan, p, c, &lock)
		defer wg.Done()
	}()
	go func() {
		reportState(&turn, &world, c.events, finishedR, &lock)
		defer wg.Done()
	}()

	outChans := make([]chan [][]byte, p.Threads)
	for i := 0; i < p.Threads; i++ {
		outChans[i] = make(chan [][]byte)
	}

	// Main game loop
	done := false
	for !done && turn < p.Turns {
		select {
		case <-quit:
			fmt.Println("-- quitting")
			done = true
		case paused := <-pauseChan:
			if paused {
				<-pauseChan
			}
		default:
			// advance to next state

			newWorld := simulateTurn(world, p, outChans)
			lock.Lock()

			//saveOutput(world, turn, p, c)
			var flipped []util.Cell
			for i := 0; i < p.ImageHeight; i++ {
				for j := 0; j < p.ImageWidth; j++ {
					if world[i][j] != newWorld[i][j] {
						flipped = append(flipped, util.Cell{X: j, Y: i})
					}
				}
			}
			world = newWorld
			c.events <- CellsFlipped{turn, flipped}
			c.events <- TurnComplete{turn}
			turn++
			lock.Unlock()
		}
	}

	finishedL <- true
	finishedR <- true

	// Wait for goroutines to confirm closure
	wg.Wait()

	close(finishedL)
	close(finishedR)
	close(pauseChan)
	close(quit)

	// output final image
	saveOutput(world, turn, p, c)
	aliveCells := getAliveCells(world)
	c.events <- FinalTurnComplete{turn, aliveCells}
	c.events <- StateChange{turn, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	close(c.ioCommand)

}

// Receive a board and slice it up into jobs
func simulateTurn(world [][]byte, p Params, outChans []chan [][]byte) [][]byte {

	// initialise output channels
	var newWorld [][]byte

	// distribution logic
	incrementY := p.ImageHeight / p.Threads
	startY := 0
	for i := 0; i < p.Threads; i++ {
		var endY int
		if i == p.Threads-1 {
			endY = p.ImageHeight
		} else {
			endY = incrementY + startY
		}
		go worker(world, outChans[i], startY, endY, p)
		startY += incrementY
	}

	// piece together new output
	for i := 0; i < p.Threads; i++ {
		result := <-outChans[i]
		newWorld = append(newWorld, result...)
	}
	return newWorld
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

func worker(world [][]byte, outChan chan<- [][]byte, startY int, endY int, p Params) {
	// initialise 2D slice of rows
	width := p.ImageWidth
	height := p.ImageHeight

	newWorld := make([][]byte, endY-startY)
	for i := startY; i < endY; i++ {
		// initialise row, set the contents of the row accordingly
		newWorld[i-startY] = make([]byte, width)
		for j := 0; j < width; j++ {
			cell := util.Cell{X: j, Y: i}
			next := getNextCell(cell, world, width, height)
			newWorld[i-startY][j] = next
		}
	}

	outChan <- newWorld
}

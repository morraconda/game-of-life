package gol

import (
	"fmt"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"uk.ac.bris.cs/gameoflife/util"
)

// to prevent deadlock, always attempt grab mutexes in this order
var pauseMutex sync.Mutex
var stateMutex sync.Mutex // for world and turncount
var ioSpacesRemaining sync.Mutex
var ioWorkAvailable sync.Mutex

// get filename from params
func getInputFilename(p Params) string {
	return fmt.Sprintf("%dx%d", p.ImageWidth, p.ImageHeight)
}

func getOutputFilename(p Params, t int) string {
	return fmt.Sprintf("%dx%dx%d", p.ImageWidth, p.ImageHeight, t)
}

// gets initial 2D slice from input
func getInitialWorld(ip *ioParams, p Params) [][]byte {
	ioSpacesRemaining.Lock()
	var in []byte
	ip.input = in
	ip.filename = getInputFilename(p)
	ip.command = ioInput
	ioWorkAvailable.Unlock()
	// initialise 2D slice of rows
	ioSpacesRemaining.Lock()
	world := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageHeight; i++ {
		// initialise row, set the contents of the row accordingly
		world[i] = make([]byte, p.ImageWidth)
		for j := 0; j < p.ImageWidth; j++ {
			current := i*p.ImageWidth + j
			world[i][j] = ip.input[current]
		}
	}
	ioSpacesRemaining.Unlock()
	return world
}

// writes board state to ioParams
func writeToIO(world [][]byte, p Params, ip *ioParams) {
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			ip.output[i][j] = world[i][j]
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
func reportState(turn *int, world *[][]byte, c chan<- Event, quit *bool) {
	for !*quit {
		stateMutex.Lock()
		if *turn > 0 {
			alive := getAliveCells(*world)
			c <- Event(AliveCellsCount{*turn, len(alive)})
		}
		stateMutex.Unlock()
		time.Sleep(2 * time.Second)
	}
}

func saveOutput(world [][]byte, turn int, p Params, ip *ioParams, events chan<- Event) {
	ioSpacesRemaining.Lock()
	ip.command = ioOutput
	ip.filename = getOutputFilename(p, turn)
	writeToIO(world, p, ip)
	ioWorkAvailable.Unlock()
	// fmt.Println("-- successfully wrote to output")

	// send write event
	ioSpacesRemaining.Lock()
	// fmt.Println("-- sending image event")
	events <- ImageOutputComplete{turn, ip.filename}
	ioSpacesRemaining.Unlock()
}

func handleKeypress(keypresses <-chan rune, turn *int, world *[][]byte,
	quit *bool, p Params, ip *ioParams, eventChan chan<- Event) {
	paused := false
	for !*quit {
		key := <-keypresses
		switch key {
		case sdl.K_s: // save
			// fmt.Println("-- s pressed")
			stateMutex.Lock()
			saveOutput(*world, *turn, p, ip, eventChan)
			stateMutex.Unlock()

		case sdl.K_q: // quit
			// fmt.Println("-- q pressed")
			if paused {
				pauseMutex.Unlock()
			}
			*quit = true

		case sdl.K_p: // pause
			// fmt.Println("-- p pressed")
			if paused {
				stateMutex.Lock()
				// fmt.Println("-- continuing")
				eventChan <- StateChange{*turn, Executing}
				paused = false
				pauseMutex.Unlock()
			} else {
				pauseMutex.Lock()
				stateMutex.Lock()
				// fmt.Println("-- paused on Turn", *turn)
				eventChan <- StateChange{*turn, Paused}
				paused = true
			}
			stateMutex.Unlock()
		}
	}
}

// distributor executes all turns
func distributor(p Params, ip *ioParams, events chan<- Event, keypresses <-chan rune) {
	// Get initial world
	ip.command = ioInput
	ip.filename = getInputFilename(p)
	world := getInitialWorld(ip, p)
	turn := 0
	quit := false

	// get the initial alive cells and use them as input to CellsFlipped
	initialFlippedCells := getAliveCells(world)
	events <- CellsFlipped{turn, initialFlippedCells}
	events <- StateChange{turn, Executing}

	// Start alive cells ticker and keypress handler
	go handleKeypress(keypresses, &turn, &world, &quit, p, ip, events)
	go reportState(&turn, &world, events, &quit)

	// fmt.Println("-- init completed")
	// Main game loop
	stateMutex.Lock()
	for !quit && turn < p.Turns {
		stateMutex.Unlock()
		// get next state
		newWorld := simulateTurn(world, p)
		// advance to next state
		pauseMutex.Lock()
		stateMutex.Lock()
		flipped := getFlippedCells(world, newWorld, p)
		events <- CellsFlipped{turn, flipped}
		events <- TurnComplete{turn}
		world = newWorld
		turn++
		pauseMutex.Unlock()
	}
	stateMutex.Unlock()
	quit = true
	// fmt.Println("-- main loop exited")

	// output final image
	saveOutput(world, turn, p, ip, events)
	aliveCells := getAliveCells(world)
	events <- FinalTurnComplete{turn, aliveCells}
	events <- StateChange{turn, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(events)
	ioSpacesRemaining.Lock()
	ip.command = ioQuit
	ioWorkAvailable.Unlock()
	ioSpacesRemaining.Lock()
	ioSpacesRemaining.Unlock()
	ioWorkAvailable.Unlock()
}

// Receive a board and slice it up into jobs
func simulateTurn(world [][]byte, p Params) [][]byte {
	// initialise next world
	newWorld := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageHeight; i++ {
		newWorld[i] = make([]byte, p.ImageWidth)
	}
	var wg sync.WaitGroup
	wg.Add(p.Threads)
	// distribution logic
	incrementY := p.ImageHeight / p.Threads

	for i := 0; i < p.Threads; i++ {
		startY := i * incrementY
		var endY int
		if i == p.Threads-1 {
			endY = p.ImageHeight
		} else {
			endY = (i + 1) * incrementY
		}
		go func() {
			worker(world, newWorld, startY, endY, p)
			wg.Done()
		}()
	}
	// wait for all goroutines to return
	wg.Wait()
	return newWorld
}

// no mutexes should be required here as each cell should only be written to by
// one worker
func worker(world [][]byte, newWorld [][]byte, startY int, endY int, p Params) {
	// initialise 2D slice of rows
	width := p.ImageWidth
	height := p.ImageHeight
	for i := startY; i < endY; i++ {
		// initialise row, set the contents of the row accordingly
		for j := 0; j < width; j++ {
			// check for flipped cells
			cell := util.Cell{X: j, Y: i}
			next := getNextCell(cell, world, width, height)
			newWorld[i][j] = next
		}
	}
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

func getFlippedCells(oldWorld [][]byte, newWorld [][]byte, p Params) []util.Cell {
	var flipped []util.Cell
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			if oldWorld[i][j] != newWorld[i][j] {
				flipped = append(flipped, util.Cell{X: j, Y: i})
			}
		}
	}
	return flipped
}

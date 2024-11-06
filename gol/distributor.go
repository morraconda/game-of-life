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
var ioMutex sync.Mutex

// get filename from params
func getInputFilename(p Params) string {
	return fmt.Sprintf("%dx%d", p.ImageWidth, p.ImageHeight)
}

func getOutputFilename(p Params, t int) string {
	return fmt.Sprintf("%dx%dx%d", p.ImageWidth, p.ImageHeight, t)
}

// gets initial 2D slice from input
func getInitialWorld(ip ioParams, p Params) [][]byte {
	// initialise 2D slice of rows
	world := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageHeight; i++ {
		// initialise row, set the contents of the row accordingly
		world[i] = make([]byte, p.ImageWidth)
		for j := 0; j < p.ImageWidth; j++ {
			current = i * p.ImageWidth + j
			world[i][j] = ip.input[current]
		}
	}
	return world
}

// writes board state to output
func writeToOutput(world [][]byte, turn int, p Params, filename string, ip ioParams) {
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			ip[i][j] = world[i][j]
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
		time.Sleep(2 * time.Second)
		stateMutex.Lock()
		alive := getAliveCells(*world)
		c <- Event(AliveCellsCount{*turn, len(alive)})
		stateMutex.Unlock()
	}
}

func saveOutput(world [][]byte, turn int, p Params, ip ioParams, events <-chan Event) {
	stateMutex.Lock()
	ioMutex.Lock()
	ip.filename := getOutputFilename(p, turn)
	writeToOutput(world, turn, p, c.ioOutput, filename)
	fmt.Println("-- successfully wrote to output")
	ioMutex.Unlock()
	// send write event
	fmt.Println("-- sending image event")
	c.events <- ImageOutputComplete{turn, filename}
	stateMutex.Unlock()
}

func handleKeypress(keypresses <-chan rune, turn *int, world *[][]byte, 
	quit *bool, p Params, c distributorChannels) {
	paused := false
	for !*quit {
		case key := <-keypresses:
			switch key {
			case sdl.K_s: // save
				fmt.Println("-- s pressed")
				stateMutex.Lock()
				saveOutput(*world, *turn, p, c)
				stateMutex.Unlock()

			case sdl.K_q: // quit
				fmt.Println("-- q pressed")
				if paused {
					pauseMutex.Unlock()
				}
				*quit = true

			case sdl.K_p: // pause
				fmt.Println("-- p pressed")
				stateMutex.Lock()
				if paused {
					fmt.Println("-- continuing")
					c.events <- StateChange{*turn, Executing}
					paused = false
					pauseMutex.Unlock()
				} else {
					pauseMutex.Lock()
					fmt.Println("-- paused on Turn", *turn)
					c.events <- StateChange{*turn, Paused}
					paused = true
				}
				stateMutex.Unlock()
				paused = !paused		
			}
	}
}

// distributor executes all turns
func distributor(p Params, ip ioParams, events chan<- Event, keypresses <-chan rune) {
	// Get initial world
	ip.command <- ioInput
	ip.filename <- getInputFilename(p)
	world := getInitialWorld(c.ioInput, p)

	quit := false
	turn := 0
	var wg sync.WaitGroup

	// get the initial alive cells and use them as input to CellsFlipped
	initialFlippedCells := getAliveCells(world)
	c.events <- CellsFlipped{turn, initialFlippedCells}
	c.events <- StateChange{turn, Executing}

	// Start alive cells ticker and keypress handler
	wg.Add(2)
	go func() {
		handleKeypress(keypresses, &turn, &world, &quit, p, c)
		defer wg.Done()
	}()
	go func() {
		reportState(&turn, &world, c.events, &quit)
		defer wg.Done()
	}()

	// Main game loop
	for !quit && turn < p.Turns {
		// give other goroutines priority to grab mutexes
		time.Sleep(100 * time.Millisecond) 
		pauseMutex.Lock()
		// get next state
		newWorld := simulateTurn(world, p)
		// advane to next state
		stateMutex.Lock()
		flipped := getFlippedCells(world, newWorld)
		c.events <- CellsFlipped{turn, flipped}
		c.events <- TurnComplete{turn}
		world = newWorld
		turn++
		pauseMutex.Unlock()
		stateMutex.Unlock()
	}

	// Wait for goroutines to confirm closure
	wg.Wait()
	fmt.Println("-- hiiiii")

	// output final image
	saveOutput(world, turn, p, c)
	aliveCells := getAliveCells(world)
	c.events <- FinalTurnComplete{turn, aliveCells}
	c.events <- StateChange{turn, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)

	// Make sure that the Io has finished any output before exiting.
	fmt.Println("-- hiiiiiiiiiiiiiii")
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	close(c.ioCommand)

	fmt.Println("-- we did it yippppeeee")
}

// Receive a board and slice it up into jobs
func simulateTurn(world [][]byte, p Params) [][]byte {
	// initialise next world
	newWorld := make([][]byte, p.ImageHeight)
	for i:=0; i < p.ImageHeight; i++ {
		newWorld[i] = make([]byte, p.ImageWidth)
	}
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
		go worker(world, newWorld, startY, endY, p)
		startY += incrementY
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

func getFlippedCells(oldWorld [][]byte, newWorld [][]byte) []util.Cell {
	var flipped []util.Cell
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			if world[i][j] != newWorld[i][j] {
				flipped = append(flipped, util.Cell{X: j, Y: i})
			}
		}
	}
	return flipped
}

// no mutexes should be required here as each cell should only be written to by 
// one worker
func worker(world [][]byte, newWorld [][]byte, startY int, endY int, p Params) {
	// initialise 2D slice of rows
	width := p.ImageWidth
	height := p.ImageHeight
	for i := startY; i < endY; i++ {
		// initialise row, set the contents of the row accordingly
		newWorld[i-startY] = make([]byte, width)
		for j := 0; j < width; j++ {
			// check for flipped cells
			cell := util.Cell{X: j, Y: i}
			next := getNextCell(cell, world, width, height)
			newWorld[i-startY][j] = next
		}
	}
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

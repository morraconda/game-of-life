package gol

import (
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
	"net/rpc"
	"sync"
	"time"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

var superMX sync.RWMutex

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
func reportState(turn *int, world *[][]byte, c chan<- Event, finished <-chan bool, wg *sync.WaitGroup) {
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

func saveOutput(client *rpc.Client, turn int, p Params, c distributorChannels) (world [][]byte) {
	output := new(stubs.Update)
	status := new(stubs.StatusReport)
	// get most recent output from broker
	err := client.Call(stubs.Finish, status, &output)
	if err != nil {
		fmt.Println("Error: ", err)
	}
	// write to output
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	c.ioCommand <- ioOutput
	c.ioFilename <- getOutputFilename(p, turn)
	writeToOutput(output.World, turn, p, c.ioOutput)
	// close io
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// send write event
	c.events <- ImageOutputComplete{turn, getOutputFilename(p, turn)}
	return output.World
}

func handleKeypress(keypresses <-chan rune, turn *int, world *[][]byte, finished <-chan bool,
	quit chan<- bool, pause chan<- bool, p Params, c distributorChannels, client *rpc.Client, wg *sync.WaitGroup) {
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
				saveOutput(client, *turn, p, c)
				superMX.Unlock()
				if !paused {
					pause <- false
				}
			case sdl.K_q: // quit
				if !paused {
					pause <- true
				}
				superMX.Lock()
				world := saveOutput(client, *turn, p, c)
				c.events <- FinalTurnComplete{*turn, getAliveCells(world)}
				c.events <- StateChange{*turn, Quitting}
				superMX.Unlock()
				quit <- true
				pause <- false
			case sdl.K_k: // shutdown
				if !paused {
					pause <- true
				}
				superMX.Lock()
				saveOutput(client, *turn, p, c)
				// Call broker to shut itself down
				status := new(stubs.StatusReport)
				err := client.Call(stubs.Close, status, &status)
				if err != nil {
					fmt.Println("Error: ", err)
				}
				c.events <- FinalTurnComplete{*turn, getAliveCells(*world)}
				c.events <- StateChange{*turn, Quitting}
				superMX.Unlock()
				quit <- true
				pause <- false
			case sdl.K_p: // pause
				if paused {
					paused = false
					fmt.Println("continuing")
					superMX.Lock()
					c.events <- StateChange{*turn, Executing}
					superMX.Unlock()
					pause <- false
				} else {
					paused = true
					fmt.Println(*turn)
					superMX.Lock()
					c.events <- StateChange{*turn, Paused}
					superMX.Unlock()
					pause <- true
				}
			}
		}
	}
}

// distributor executes all turns, sends work to broker
func distributor(p Params, c distributorChannels, keypresses <-chan rune) {
	// Dial broker
	client, _ := rpc.Dial("tcp", "127.0.0.1:8030")
	// Get initial world
	c.ioCommand <- ioInput
	c.ioFilename <- getInputFilename(p)
	world := getInitialWorld(c.ioInput, p)
	// Pack initial world to be sent to broker
	status := new(stubs.StatusReport)
	input := new(stubs.Input)
	input.World = world
	input.Width = p.ImageWidth
	input.Height = p.ImageHeight
	input.Threads = p.Threads
	// Send initial world to broker
	err := client.Call(stubs.Start, input, &status)
	// Channels for communicating with ticker and keypress handler
	wg := sync.WaitGroup{}
	wg.Add(2)
	quit := make(chan bool, 1)
	finishedR := make(chan bool)
	finishedL := make(chan bool)
	pause := make(chan bool)
	turn := 0
	// Start alive cells ticker and keypress handler
	go handleKeypress(keypresses, &turn, &world, finishedL, quit, pause, p, c, client, &wg)
	go reportState(&turn, &world, c.events, finishedR, &wg)
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
			// Call broker to advance to next state
			superMX.Lock()
			update := new(stubs.Update)
			// f := update.Flipped
			err := client.Call(stubs.NextState, status, &update)
			if err != nil {
				panic(err)
			}

			// TODO: move this into helper function and remove flipped cell computation from broker?
			var f []util.Cell
			for i := 0; i < len(world); i++ {
				for j := 0; j < len(world[i]); j++ {
					if world[i][j] != update.World[i][j] {
						f = append(f, util.Cell{j, i})
					}
				}
			}

			world = update.World

			c.events <- CellsFlipped{turn, f}
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
		world := saveOutput(client, turn, p, c)
		aliveCells := getAliveCells(world)
		c.events <- FinalTurnComplete{turn, aliveCells}
		c.events <- StateChange{turn, Quitting}
		superMX.Unlock()
	}

	// Close client
	err = client.Close()
	if err != nil {
		fmt.Println("Error: ", err)
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
	return
}

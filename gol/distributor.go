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
func writeToOutput(world [][]byte, turn int, p Params,
	eventChan chan<- Event, outputChan chan<- byte) {
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			outputChan <- world[i][j]
		}
	}
	//eventChan <- ImageOutputComplete{turn, getOutputFilename(p, turn)}
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
			return
		case <-time.After(2 * time.Second):
			alive := getAliveCells(*world)
			c <- Event(AliveCellsCount{*turn, len(alive)})
		}
	}
}

func saveOutput(client *rpc.Client, turn int, p Params, c distributorChannels) (world [][]byte) {
	output := new(stubs.Output)
	status := new(stubs.StatusReport)
	// get most recent output from broker
	err := client.Call(stubs.Finish, status, &output)
	if err != nil {
		fmt.Println("Error: ", err)
	}
	// write to output
	c.ioCommand <- ioOutput
	c.ioFilename <- getOutputFilename(p, turn)
	writeToOutput(output.World, turn, p, c.events, c.ioOutput)
	// close io
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// send write event
	c.events <- ImageOutputComplete{turn, getOutputFilename(p, turn)}
	return output.World
}

//TODO: get s, q, k working
func handleKeypress(keypresses <-chan rune, turn *int, world *[][]byte, finished <-chan bool,
	quit chan<- bool, pause chan<- bool, p Params, c distributorChannels, client *rpc.Client) {
	paused := false
	for {
		select {
		case <-finished:
			return
		case key := <-keypresses:
			switch key {
			case sdl.K_s: // save
				if !paused {
					pause <- true
				}
				saveOutput(client, *turn, p, c)
				if !paused {
					pause <- false
				}
			case sdl.K_q: // quit
				if !paused {
					pause <- true
				}
				world := saveOutput(client, *turn, p, c)
				c.events <- FinalTurnComplete{*turn, getAliveCells(world)}
				c.events <- StateChange{*turn, Quitting}
				quit <- true
				pause <- false
				//return
			case sdl.K_k: // shutdown
				// TODO: shut down broker
				if !paused {
					pause <- true
				}
				saveOutput(client, *turn, p, c)
				c.events <- FinalTurnComplete{*turn, getAliveCells(*world)}
				c.events <- StateChange{*turn, Quitting}
				quit <- true
				pause <- false
				//return
			case sdl.K_p: // pause
				if paused {
					paused = false
					fmt.Println("continuing")
					c.events <- StateChange{*turn, Executing}
					pause <- false
				} else {
					paused = true
					fmt.Println(*turn)
					c.events <- StateChange{*turn, Paused}
					pause <- true
				}
			}
		}
	}
}

// distributor executes all turns, sends work to broker
func distributor(p Params, c distributorChannels, keypresses <-chan rune) {
	client, _ := rpc.Dial("tcp", "127.0.0.1:8030")
	// init crap
	c.ioCommand <- ioInput
	c.ioFilename <- getInputFilename(p)
	world := getInitialWorld(c.ioInput, p)
	status := new(stubs.StatusReport)
	input := new(stubs.Input)
	input.World = world
	input.Width = p.ImageWidth
	input.Height = p.ImageHeight
	input.Threads = p.Threads
	err := client.Call(stubs.Start, input, &status)
	turn := 0
	wg := sync.WaitGroup{}
	wg.Add(1)
	quit := make(chan bool)
	finishedR := make(chan bool)
	finishedL := make(chan bool)
	pause := make(chan bool)
	// start alive cells ticker and keypress handler
	go func() {
		go handleKeypress(keypresses, &turn, &world, finishedL, quit, pause, p, c, client)
		go reportState(&turn, &world, c.events, finishedR)
		wg.Done()
	}()
	// execute all turns of the Game of Life.
	exit := false
	c.events <- StateChange{turn, Executing}

mainLoop:
	for turn < p.Turns {
		select {
		case paused := <-pause:
			for paused {
				paused = <-pause
			}
		case <-quit:
			exit = true
			break mainLoop
		default:
			fmt.Println("turn\n")
			flipped := new(stubs.Update)
			err := client.Call(stubs.NextState, status, &flipped)
			if err != nil {
				panic(err)
			}

			c.events <- CellsFlipped{turn, flipped.Flipped}
			turn++
		}
	}
	fmt.Println("exit loop\n")
	finishedR <- true
	finishedL <- true
	if !exit {
		world := saveOutput(client, turn, p, c)
		aliveCells := getAliveCells(world)
		c.events <- FinalTurnComplete{turn, aliveCells}
		c.events <- StateChange{turn, Quitting}
	}

	err = client.Close()
	if err != nil {
		fmt.Println("Error: ", err)
	}
	fmt.Println("Waiting")
	wg.Wait()
	fmt.Println("Wait over")
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

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

func getOutputFilename(p Params) string {
	return fmt.Sprintf("%dx%dx%d", p.ImageWidth, p.ImageHeight, p.Turns)
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

// TODO: get q and k to work
func handleKeypress(turn *int, world *[][]byte, eventChan chan<- Event,
	outputChan chan<- byte, finished <-chan bool, quit chan<- bool, pause *sync.WaitGroup, p Params) {
	paused := false
	for {
		select {
		case <-finished:
			return
		default:
			for event := sdl.PollEvent(); event != nil; event = sdl.PollEvent() {
				switch k := event.(type) {
				case *sdl.KeyboardEvent:
					if k.State == sdl.PRESSED {
						switch k.Keysym.Sym {
						case sdl.K_s:
							writeToOutput(*world, *turn, p, eventChan, outputChan)
						case sdl.K_q:
							if paused {
								pause.Done()
							}
							quit <- true
							return
						case sdl.K_k:
							if paused {
								pause.Done()
							}
							quit <- true
							writeToOutput(*world, *turn, p, eventChan, outputChan)
							return
						case sdl.K_p:
							if paused {
								fmt.Println("continuing")
								paused = false
								pause.Done()
							} else {
								fmt.Println(*turn)
								paused = true
								pause.Add(1)
							}

						}
					}
				}

			}
		}
	}
}

// distributor executes all turns, sends work to broker
func distributor(p Params, c distributorChannels) {
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
	if err != nil {
		fmt.Println("Error: ", err)
	}

	err = sdl.Init(sdl.INIT_EVERYTHING)
	if err != nil {
		panic(err)
	}
	window, err := sdl.CreateWindow("Game Of Life",
		sdl.WINDOWPOS_UNDEFINED, sdl.WINDOWPOS_UNDEFINED,
		int32(p.ImageWidth), int32(p.ImageHeight), sdl.WINDOW_SHOWN)
	if err != nil {
		panic(err)
	}

	turn := 0
	wg := sync.WaitGroup{}
	wg.Add(1)
	// start alive cells ticker and keypress handler
	quit := make(chan bool)
	finished := make(chan bool)
	pause := sync.WaitGroup{}
	go handleKeypress(&turn, &world, c.events, c.ioOutput, finished, quit, &pause, p)
	go func() {
		reportState(&turn, &world, c.events, finished)
		defer wg.Done()
	}()
	exit := false
	// execute all turns of the Game of Life.
	for h := 0; h < p.Turns; h++ {
		pause.Wait()
		select {
		case <-quit:
			exit = true
			break
		default:
			flipped := new(stubs.Update)
			err := client.Call(stubs.NextState, status, &flipped)
			if err != nil {
				panic(err)
			}
			turn++
			c.events <- CellsFlipped{turn, flipped.Flipped}
			c.events <- StateChange{turn, Executing}
		}
	}
	finished <- true
	if !exit {
		output := new(stubs.Output)
		err = client.Call(stubs.Finish, status, &output)
		if err != nil {
			fmt.Println("Error: ", err)
		}
		c.ioCommand <- ioOutput
		c.ioFilename <- getOutputFilename(p)
		writeToOutput(output.World, p.Turns, p, c.events, c.ioOutput)
		aliveCells := getAliveCells(output.World)
		c.events <- FinalTurnComplete{p.Turns, aliveCells}
	}
	err = client.Close()
	if err != nil {
		fmt.Println("Error: ", err)
	}

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// close alive cells reporting
	c.events <- StateChange{p.Turns, Quitting}

	finished <- true
	window.Destroy()
	sdl.Quit()
	wg.Wait()
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)

}

package gol

import (
	"flag"
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
	"log"
	"net/rpc"
	"sync"
	"time"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

var pAddr = flag.String("ip", "127.0.0.1:8050", "IP and port to listen on")
var brokerAddr = flag.String("broker", "127.0.0.1:8030", "Address of broker instance")
var once sync.Once

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioOutput   chan<- uint8
	ioInput    <-chan uint8
}

var channel distributorChannels

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

func saveOutput(client *rpc.Client, p Params, c distributorChannels) (world [][]byte) {
	output := new(stubs.Update)
	status := new(stubs.StatusReport)
	// get most recent output from broker
	err := client.Call(stubs.GetState, status, &output)
	if err != nil {
		fmt.Println("Error calling Finish: ", err)
	}
	// write to output
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	c.ioCommand <- ioOutput
	c.ioFilename <- getOutputFilename(p, output.Turn)
	writeToOutput(output.World, output.Turn, p, c.ioOutput)
	// close io
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// send write event
	c.events <- ImageOutputComplete{output.Turn, getOutputFilename(p, output.Turn)}
	return output.World
}

// Ran synchronously, reports state to distributor every 2 seconds
func reportState(finished <-chan bool, wg *sync.WaitGroup, client *rpc.Client, c distributorChannels, initWG *sync.WaitGroup) {
	initWG.Wait()
	for {
		select {
		case <-finished:
			wg.Done()
			return
		case <-time.After(2 * time.Second):
			req := new(stubs.StatusReport)
			res := new(stubs.Update)
			err := client.Call(stubs.GetState, req, &res)
			if err != nil {
				fmt.Println("Error calling GetState in reportState: ", err)
				wg.Done()
				return
			}

			c.events <- CellsFlipped{res.Turn, res.Flipped}
			c.events <- AliveCellsCount{res.Turn, len(res.AliveCells)}
			c.events <- TurnComplete{res.Turn}

		}
	}
}

// forwards key presses to broker for handling
func handleKeypress(keypresses <-chan rune, finished <-chan bool,
	quit chan<- bool, p Params, c distributorChannels, client *rpc.Client, wg *sync.WaitGroup, initWG *sync.WaitGroup) {
	initWG.Wait()
	paused := false
	for {
		select {
		case <-finished: // stop handling
			wg.Done()
			return
		case key := <-keypresses:
			req := new(stubs.PauseData)
			res := new(stubs.PauseData)
			switch key {
			case sdl.K_s: // save
				if !paused {
					req.Value = 1
					err := client.Call(stubs.Pause, req, &res)
					if err != nil {
						fmt.Println("Error pausing in save: ", err)
					}
				}
				saveOutput(client, p, c)
				if !paused {
					req.Value = 0
					err := client.Call(stubs.Pause, req, &res)
					if err != nil {
						fmt.Println("Error un-pausing in save: ", err)
					}
				}
			case sdl.K_q: // quit
				if !paused {
					req.Value = 1
					err := client.Call(stubs.Pause, req, &res)
					if err != nil {
						fmt.Println("Error pausing in quit: ", err)
					}
				}
				saveOutput(client, p, c)
				//c.events <- FinalTurnComplete{res.Value}
				req.Value = 0
				err := client.Call(stubs.Pause, req, &res)
				if err != nil {
					fmt.Println("Error un-pausing in quit: ", err)
				}
				err = client.Call(stubs.Quit, req, &res)
				if err != nil {
					fmt.Println("Error quitting in quit: ", err)
				}
				quit <- true
			case sdl.K_k: // shutdown
				if !paused {
					req.Value = 1
					err := client.Call(stubs.Pause, req, &res)
					if err != nil {
						fmt.Println("Error pausing in shutdown: ", err)
					}
				}
				saveOutput(client, p, c)
				//c.events <- FinalTurnComplete{res.Value}
				req.Value = 0
				err := client.Call(stubs.Pause, req, &res)
				if err != nil {
					fmt.Println("Error un-pausing in quit: ", err)
				}
				err = client.Call(stubs.Quit, req, &res)
				if err != nil {
					fmt.Println("Error quitting in shutdown: ", err)
				}
				err = client.Call(stubs.ShutDown, req, &res)
				if err != nil {
					fmt.Println("Error shutting down in shutdown: ", err)
				}
				quit <- true
			case sdl.K_p: // pause
				if paused {
					paused = false
					req.Value = 0
					err := client.Call(stubs.Pause, req, &res)
					if err != nil {
						fmt.Println("Error un-pausing in pause: ", err)
					}
					fmt.Println("continuing")
					c.events <- StateChange{res.Value, Executing}
				} else {
					paused = true
					req.Value = 1
					err := client.Call(stubs.Pause, req, &res)
					if err != nil {
						fmt.Println("Error pausing in pause: ", err)
					}
					fmt.Println(res.Value)
					c.events <- StateChange{res.Value, Paused}

				}
			}
		}
	}
}

// distributor executes all turns, sends work to broker
func distributor(p Params, c distributorChannels, keypresses <-chan rune) {
	//Dial the broker
	client, err := rpc.Dial("tcp", *brokerAddr)
	if err != nil {
		log.Fatalf("Failed to connect to broker: %v", err)
	}

	// Get initial world
	channel = c
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
	input.Turns = p.Turns

	initWG := sync.WaitGroup{}
	initWG.Add(1)

	// Start goroutines
	wg := sync.WaitGroup{}
	wg.Add(2)
	quit := make(chan bool, 1)
	finishedR := make(chan bool, 1)
	finishedL := make(chan bool, 1)
	go handleKeypress(keypresses, finishedR, quit, p, c, client, &wg, &initWG)
	go reportState(finishedL, &wg, client, c, &initWG)

	// Initialise broker
	err = client.Call(stubs.Init, input, &status)
	if err != nil {
		fmt.Println("Error initialising in quit: ", err)
	}

	// Send initial events
	req := new(stubs.StatusReport)
	res := new(stubs.Update)
	err = client.Call(stubs.GetState, req, &res)
	if err != nil {
		fmt.Println("Error getting initial state: ", err)
	}
	c.events <- CellsFlipped{0, res.Flipped}
	c.events <- StateChange{0, Executing}

	// Run main loop in broker, block until it has finished
	call := client.Go(stubs.Start, status, &status, nil)
	initWG.Done()
	select {
	case <-quit:
	case <-call.Done:
		saveOutput(client, p, c)
	}

	// Signal goroutines to close
	finishedR <- true
	finishedL <- true

	// Send final events
	req = new(stubs.StatusReport)
	res = new(stubs.Update)
	err = client.Call(stubs.GetState, req, &res)
	if err != nil {
		fmt.Println("Error getting final state: ", err)
	}
	c.events <- FinalTurnComplete{res.Turn, res.AliveCells}
	c.events <- StateChange{res.Turn, Quitting}

	// Wait until goroutines have closed
	wg.Wait()

	// Close client
	err = client.Close()
	if err != nil {
		fmt.Println("Error closing connection: ", err)
	}

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
	return
}

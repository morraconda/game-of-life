package gol

import (
	"flag"
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
	"log"
	"net"
	"net/rpc"
	"sync"
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

func saveOutput(client *rpc.Client, p Params, c distributorChannels, quitting bool) (world [][]byte) {
	fmt.Println("SAVING")
	output := new(stubs.Update)
	status := new(stubs.StatusReport)
	// get most recent output from broker
	err := client.Call(stubs.Finish, status, &output)
	if err != nil {
		fmt.Println("Error: ", err)
	}
	if quitting {
		output.Turn = output.Turn + 1
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

// forwards key presses to broker for handling
func handleKeypress(keypresses <-chan rune, finished <-chan bool,
	quit chan<- bool, p Params, c distributorChannels, client *rpc.Client, wg *sync.WaitGroup) {
	req := new(stubs.KeyPress)
	req.Paused = false
	res := new(stubs.StatusReport)
	paused := false
	for {
		select {
		case <-finished: // stop handling
			wg.Done()
			return
		case key := <-keypresses:
			fmt.Println("KEY")
			switch key {
			case sdl.K_s: // save
				req.Key = "s"
				err := client.Call(stubs.HandleKey, req, &res)
				if err != nil {
					panic(err)
				}
				saveOutput(client, p, c, false)
			case sdl.K_q: // quit
				req.Key = "q"
				saveOutput(client, p, c, true)
				err := client.Call(stubs.HandleKey, req, &res)
				if err != nil {
					panic(err)
				}
				fmt.Println("QUIT")
				wg.Done()
				quit <- true
				return
			case sdl.K_k: // shutdown
				req.Key = "k"
				saveOutput(client, p, c, true)
				err := client.Call(stubs.HandleKey, req, &res)
				if err != nil {
					panic(err)
				}
				wg.Done()
				quit <- true
				return
			case sdl.K_p: // pause
				req.Paused = paused
				if paused {
					paused = false
					req.Key = "p"
					err := client.Call(stubs.HandleKey, req, &res)
					if err != nil {
						panic(err)
					}
				} else {
					paused = true
					req.Key = "p"
					err := client.Call(stubs.HandleKey, req, &res)
					if err != nil {
						panic(err)
					}
					fmt.Println(res.Message)
				}
			}
		}
	}
}

type Control struct{}

// Event forwards events from the broker to SDL
func (b *Control) Event(req stubs.Event, res *stubs.StatusReport) (err error) {
	if req.Type == "StateChange" {
		if req.State == "Executing" {
			channel.events <- StateChange{req.Turn, Executing}
		} else if req.State == "Paused" {
			channel.events <- StateChange{req.Turn, Paused}
		} else if req.State == "Quitting" {
			channel.events <- StateChange{req.Turn, Executing}
		}
	} else if req.Type == "CellsFlipped" {
		channel.events <- CellsFlipped{req.Turn, req.Cells}
	} else if req.Type == "AliveCellsCount" {
		channel.events <- AliveCellsCount{req.Turn, req.Count}
	} else if req.Type == "TurnComplete" {
		channel.events <- TurnComplete{req.Turn}
	} else if req.Type == "FinalTurnComplete" {
		channel.events <- FinalTurnComplete{req.Turn, req.Cells}
	} else {
		fmt.Println("Error: unknown event type")
	}
	return
}

// distributor executes all turns, sends work to broker
func distributor(p Params, c distributorChannels, keypresses <-chan rune) {

	// Perform server init if first time running
	once.Do(func() {
		flag.Parse()
		// Register own public functions
		err := rpc.Register(&Control{})
		if err != nil {
			log.Fatalf("Failed to register Compute service: %v", err)
		}

		//Start listener
		go func() {
			listener, err := net.Listen("tcp", *pAddr)
			if err != nil {
				log.Fatalf("Failed to listen on %s: %v", *pAddr, err)
			}
			defer listener.Close()
			rpc.Accept(listener)
		}()
	})

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
	input.Callback = "Control.Event"
	input.Address = *pAddr
	input.Turns = p.Turns

	// Start goroutines
	wg := sync.WaitGroup{}
	wg.Add(1)
	quit := make(chan bool, 1)
	finished := make(chan bool, 1)

	err = client.Call(stubs.Init, input, &status)
	if err != nil {
	}
	go handleKeypress(keypresses, finished, quit, p, c, client, &wg)
	// Run broker, block until it has finished
	call := client.Go(stubs.Start, status, &status, nil)
	select {
	case <-quit:
	case <-call.Done:

		saveOutput(client, p, c, false)
	}
	fmt.Println("Exited loop")

	// Tell keyhandler to close and wait until it does
	finished <- true
	wg.Wait()

	// Close client
	err = client.Close()
	if err != nil {
		fmt.Println("Error: ", err)
	}

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
	return
}

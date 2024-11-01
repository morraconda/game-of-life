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

var superMX sync.RWMutex

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

func handleKeypress(keypresses <-chan rune, finished <-chan bool,
	quit chan<- bool, pause chan<- bool, p Params, c distributorChannels, client *rpc.Client, wg *sync.WaitGroup) {
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
			switch key {
			case sdl.K_s: // save
				if !paused {
					req.Paused = true
				}
				req.Key = "s"
				client.Call(stubs.HandleKey, req, &res)
				// add save
			case sdl.K_q: // quit
				if !paused {
					req.Paused = true
				}
				req.Key = "q"
				client.Call(stubs.HandleKey, req, &res)
				// add save
			case sdl.K_k: // shutdown
				if !paused {
					req.Paused = true
				}
				req.Key = "k"
				client.Call(stubs.HandleKey, req, &res)
				// add save
			case sdl.K_p: // pause
				if paused {
					paused = false
					req.Key = "p"
					client.Call(stubs.HandleKey, req, &res)
					fmt.Println("continuing")
					pause <- false
				} else {
					paused = true
					req.Key = "p"
					client.Call(stubs.HandleKey, req, &res)
					fmt.Println(res)
					pause <- true
				}
			}
		}
	}
}

// reports state every 2 seconds

type Control struct{}

func (b *Control) Event(req stubs.Event, res *stubs.StatusReport) (err error) {
	channel.events <- req
	return
}

// distributor executes all turns, sends work to broker
func distributor(p Params, c distributorChannels, keypresses <-chan rune) {
	pAddr := flag.String("ip", "127.0.0.1:8050", "IP and port to listen on")
	brokerAddr := flag.String("broker", "127.0.0.1:8030", "Address of broker instance")
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

	//Dial the broker
	client, err := rpc.Dial("tcp", *brokerAddr)
	if err != nil {
		log.Fatalf("Failed to connect to broker: %v", err)
	}

	channel = c
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
	input.Callback = "Control.Event"
	input.Address = *pAddr

	// Start goroutines
	wg := sync.WaitGroup{}
	wg.Add(1)
	quit := make(chan bool, 1)
	finishedL := make(chan bool)
	pause := make(chan bool)
	go handleKeypress(keypresses, finishedL, quit, pause, p, c, client, &wg)

	// Start broker iteration
	err = client.Call(stubs.Start, input, &status)
	if err != nil {
		fmt.Println("Error: ", err)
	}

	//TODO: BLOCK HERE

	// Close client
	err = client.Close()
	if err != nil {
		fmt.Println("Error: ", err)
	}

	// Tell goroutine to close and wait until they do
	finishedL <- true
	wg.Wait()
	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
	return
}

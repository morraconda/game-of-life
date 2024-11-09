package gol

import (
	"flag"
	"fmt"
	"github.com/veandco/go-sdl2/sdl"
	"log"
	"net/rpc"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

//TODO: add switch to disable visualisation
//TODO: fix phantom bug that appears when running benchmark
//TODO: fix quit not working after client reconnects:
//TODO: add continuous state save for recovery
// this is due to Ctrl+C not exiting cleanly and leaving the events channel opening causing deadlock?
// could also do with figuring out why the hell a q keypress is sometimes sent when ctrl+C is used

var brokerAddr = flag.String("broker", "127.0.0.1:8030", "Address of broker instance")

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

// Cache the state
func makeCheckpoint(client *rpc.Client, p Params) {
	pwd, _ := os.Getwd()
	path := fmt.Sprintf("%s/gol/tmp/%s.cache", pwd, getInputFilename(p))
	err := *new(error)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0644)
	defer file.Close()
	if err != nil {
		fmt.Println("error opening: ", path, err)
		return
	}
	output := new(stubs.Update)
	status := new(stubs.StatusReport)
	// get most recent output from broker
	err = client.Call(stubs.GetState, status, &output)
	if err != nil {
		fmt.Println("Error calling broker in makeCheckpoint: ", err)
		return
	}
	turnBytes := make([]byte, 4)
	turnBytes[0] = byte(output.Turn >> 24)
	turnBytes[1] = byte(output.Turn >> 16)
	turnBytes[2] = byte(output.Turn >> 8)
	turnBytes[3] = byte(output.Turn)

	_, err = file.Write(turnBytes)
	if err != nil {
		fmt.Println("Error writing turn number in makeCheckPoint: ", err)
		return
	}
	for i := 0; i < p.ImageHeight; i++ {
		_, err = file.Write(output.World[i])
		if err != nil {
			fmt.Println("Error writing world in makeCheckPoint: ", err)
			return
		}
	}
	return

}

// If a cached state exists, load it
func getCheckpoint(p Params, world *[][]byte, turn *int) {
	pwd, _ := os.Getwd()
	path := fmt.Sprintf("%s/gol/tmp/%s.cache", pwd, getInputFilename(p))
	if _, err := os.Stat(path); err == nil {
		file, err := os.OpenFile(path, os.O_RDONLY, 0644)
		defer file.Close()
		if err != nil {
			return
		}
		turnBytes := make([]byte, 4)
		_, err = file.Read(turnBytes)
		if err != nil {
			fmt.Println("Error reading turn number: ", err)
			return
		}

		cachedTurn := int(turnBytes[0])<<24 | int(turnBytes[1])<<16 | int(turnBytes[2])<<8 | int(turnBytes[3])
		fmt.Println(cachedTurn)

		if cachedTurn != 0 && cachedTurn != p.Turns {
			fmt.Println("Cached checkpoint for ", getInputFilename(p), " found, continuing execution from turn ", cachedTurn)
			//line := make([]byte, p.ImageWidth)
			for i := 0; i < p.ImageHeight; i++ {
				line := make([]byte, p.ImageWidth)
				_, err = file.Read(line)
				if err != nil {
					fmt.Println("Error reading line in makeCheckPoint: ", err)
					return
				}
				(*world)[i] = line
			}
			*turn = cachedTurn
			return
		} else {
			return
		}
	} else {
		return
	}
}

func periodicStateSave(finished <-chan bool, wg *sync.WaitGroup, client *rpc.Client, p Params, c distributorChannels, initWG *sync.WaitGroup) {
	initWG.Wait()
	for {
		select {
		case <-finished:
			wg.Done()
			return
		case <-time.After(10 * time.Second):
			fmt.Println("CACHING")
			makeCheckpoint(client, p)
		}
	}
}

// Ran synchronously, reports state to distributor every 2 seconds
// Also operates as heartbeat to check broker is still working
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
	quit chan<- bool, shutdown chan<- bool, p Params, c distributorChannels, client *rpc.Client, wg *sync.WaitGroup, initWG *sync.WaitGroup, pause bool) {
	initWG.Wait()
	paused := pause
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
				quit <- true
			case sdl.K_k: // shutdown
				shutdown <- true
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

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	c.ioCommand <- ioInput
	c.ioFilename <- getInputFilename(p)

	initWG := sync.WaitGroup{}
	initWG.Add(1)

	// Start goroutines
	wg := sync.WaitGroup{}
	wg.Add(3)
	exit := false
	quit := make(chan bool, 1)
	shutdown := make(chan bool, 1)
	finishedR := make(chan bool, 1)
	finishedL := make(chan bool, 1)
	finishedC := make(chan bool, 1)

	// Check if game is already running
	update := new(stubs.Update)
	status := new(stubs.StatusReport)
	err = client.Call(stubs.GetState, status, &update)
	if err != nil {
		fmt.Println("Error checking for existing game: ", err)
	}

	go handleKeypress(keypresses, finishedR, quit, shutdown, p, c, client, &wg, &initWG, update.Paused)
	go reportState(finishedL, &wg, client, c, &initWG)
	go periodicStateSave(finishedC, &wg, client, p, c, &initWG)

	if !update.Running {
		// Pack initial world to be sent to broker
		input := new(stubs.Input)
		input.World = getInitialWorld(c.ioInput, p)
		input.Width = p.ImageWidth
		input.Height = p.ImageHeight
		input.Threads = p.Threads
		input.Turns = p.Turns
		input.StartingTurn = 0

		// Check for existing checkpoints for this execution
		getCheckpoint(p, &input.World, &input.StartingTurn)

		// Set number of goroutines to run on each worker
		input.Routines = 4
		input.Threads = p.Threads / 4

		// Initialise broker
		err = client.Call(stubs.Init, input, &status)
		if err != nil {
			fmt.Println("Error initialising: ", err)
		}

		// Send initial events
		req := new(stubs.StatusReport)
		res := new(stubs.Update)
		err = client.Call(stubs.GetState, req, &res)
		if err != nil {
			fmt.Println("Error getting initial state: ", err)
		}
		c.events <- CellsFlipped{0, res.Flipped}
		c.events <- StateChange{res.Turn, Executing}

		client.Go(stubs.Start, status, &status, nil)

	} else {
		fmt.Println("Existing game detected, continuing")
		req := new(stubs.StatusReport)
		res := new(stubs.Update)
		err = client.Call(stubs.GetTotalFlipped, req, &res)
		c.events <- CellsFlipped{res.Turn, res.Flipped}
	}

	// Block until broker has finished processing
	block := client.Go(stubs.Executing, status, &status, nil)
	initWG.Done()
	quitted := false
	select {
	case <-sigChan:
		c.ioCommand <- ioCheckIdle
		<-c.ioIdle
		close(c.events)
		os.Exit(0)
	case <-quit:
		quitted = true
	case <-shutdown:
		quitted = true
		exit = true
	case <-block.Done:
		saveOutput(client, p, c)
	}

	if quitted {
		fmt.Println("quitted")
		req := new(stubs.PauseData)
		res := new(stubs.PauseData)
		err = client.Call(stubs.Quit, req, &res)
		if err != nil {
			fmt.Println("Error quitting: ", err)
		}
		fmt.Println("waiting for save")
		saveOutput(client, p, c) //TODO: HANGS HERE
		fmt.Println("saved")
	}

	// Signal goroutines to close
	finishedR <- true
	finishedL <- true

	// Send final events
	req := new(stubs.StatusReport)
	res := new(stubs.Update)
	err = client.Call(stubs.GetState, req, &res)
	if err != nil {
		fmt.Println("Error getting final state: ", err)
	}
	c.events <- FinalTurnComplete{res.Turn, res.AliveCells}
	c.events <- StateChange{res.Turn, Quitting}

	// Wait until goroutines have closed
	wg.Wait()

	if exit {
		placeholderReq := new(stubs.PauseData)
		placeholderRes := new(stubs.PauseData)
		err = client.Call(stubs.ShutDown, placeholderReq, &placeholderRes)
		if err != nil {
			fmt.Println("Error shutting down: ", err)
		}
	}

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

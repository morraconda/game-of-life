package main

import (
	"flag"
	"fmt"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

// Public variables

var superMX sync.RWMutex
var jobsMX sync.RWMutex
var wgMX sync.RWMutex
var pAddr *string

// Deep copy one array into another
func deepCopy(output *[][]byte, original *[][]byte) {
	for i := 0; i < len(*original); i++ {
		copy((*output)[i], (*original)[i])
	}
}

// Spawn a new worker, this worker will dial itself into the broker
func spawnWorkers(workerAddresses []*exec.Cmd, workerCount int) {
	// Allocate a random free address
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	// Start a worker at this address
	cmd := exec.Command("go", "run", "../server/server.go", "-ip=127.0.0.1:"+strconv.Itoa(port), "-broker=127.0.0.1:"+*pAddr)
	workerAddresses = append(workerAddresses, cmd)
	err = cmd.Start()
	workerCount += 1
	if err != nil {
		panic(err)
	}
}

func closeWorkers(workerAddresses []*exec.Cmd) {
	for _, worker := range workerAddresses {
		err := worker.Process.Kill()
		if err != nil {
			fmt.Println("Error: ", err)
		}
	}
	os.Exit(0)
	return
}

// Receive a board and slice it up into jobs
func publish(width int, height int, threads int, world *[][]byte, jobs chan stubs.Request, wg *sync.WaitGroup) {
	splitRequest := new(stubs.Request)
	superMX.Lock()
	splitRequest.World = *world
	splitRequest.Width, splitRequest.Height = width, height
	superMX.Unlock()
	incrementY := height / threads
	startY := 0
	for i := 0; i < threads; i++ {
		splitRequest.StartY = startY
		if i == threads-1 {
			splitRequest.EndY = height
		} else {
			splitRequest.EndY = incrementY + startY
		}
		startY += incrementY
		jobsMX.Lock()
		wgMX.Lock()
		jobs <- *splitRequest
		wg.Add(1)
		jobsMX.Unlock()
		wgMX.Unlock()
	}
}

// Routine ran once per server instance, takes jobs from the queue and sends them to the server
func subscriberLoop(client *rpc.Client, callback string, jobs chan stubs.Request, newWorld *[][]byte, flipped *[]util.Cell, wg *sync.WaitGroup) {
	for {
		//Take a job from the job queue
		job := <-jobs
		response := new(stubs.Response) // Empty response
		err := client.Call(callback, job, response)
		if err != nil {
			panic(err)
		}
		//Append the results to the new state
		superMX.Lock()
		for i := 0; i < len(response.World); i++ {
			(*newWorld)[job.StartY+i] = response.World[i]
		}
		for i := 0; i < len(response.Flipped); i++ {
			*flipped = append(*flipped, response.Flipped[i])
		}
		superMX.Unlock()
		wg.Done()
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

func reportState(finished <-chan bool, wg *sync.WaitGroup, callback string, world *[][]byte, distributor *rpc.Client, turn *int) {
	for {
		select {
		case <-finished:
			wg.Done()
			return
		case <-time.After(2 * time.Second):
			superMX.Lock()
			alive := getAliveCells(*world)
			res := new(stubs.StatusReport)
			err := distributor.Call(callback, stubs.Event{Type: "AliveCellsCount", Turn: *turn, Count: len(alive)}, &res)
			if err != nil {
				fmt.Println(err)
			}
			superMX.Unlock()
		}
	}
}

// increment game loop by one step
func nextState(width int, height int, threads int, world *[][]byte, newWorld *[][]byte, jobs chan stubs.Request, wg *sync.WaitGroup, flipped *[]util.Cell) {
	publish(width, height, threads, world, jobs, wg)
	// Wait until all jobs have been processed
	wg.Wait()
	superMX.Lock()
	deepCopy(world, newWorld)
	flipped = nil
	superMX.Unlock()
	return
}

// TODO: move global variables into struct
type Broker struct {
	workerCount     int
	workerAddresses []*exec.Cmd
	height          int
	width           int
	threads         int
	turns           int
	callback        string
	address         string
	distributor     *rpc.Client
	jobs            chan stubs.Request
	world           [][]byte
	newWorld        [][]byte
	paused          chan bool
	quit            chan bool
	turn            int
	flipped         []util.Cell
	wg              sync.WaitGroup
}

// Start Called by distributor to load initial values
func (b *Broker) Start(input stubs.Input, res *stubs.StatusReport) (err error) {
	b.jobs = make(chan stubs.Request, 64)
	b.paused = make(chan bool)
	b.quit = make(chan bool)
	superMX.Lock()
	b.height = input.Height
	b.width = input.Width
	b.world = input.World
	//deepCopy(&world, &input.World)
	b.threads = input.Threads
	b.turns = input.Turns
	b.callback = input.Callback
	b.address = input.Address
	b.newWorld = make([][]byte, b.height)
	for i := range b.newWorld {
		b.newWorld[i] = make([]byte, b.width)
	}
	superMX.Unlock()
	// If there are not enough workers currently available, spawn more
	if b.threads > b.workerCount {
		for i := 0; i < b.threads-b.workerCount; i++ {
			spawnWorkers(b.workerAddresses, b.workerCount)
		}
	}

	b.distributor, err = rpc.Dial("tcp", b.address)
	if err != nil {
		panic(err)
	}
	res = new(stubs.StatusReport)

	b.turn = 0

	//Make initial calls
	initialFlippedCells := getAliveCells(b.world)
	err = b.distributor.Call(b.callback, stubs.Event{Type: "CellsFlipped", Turn: b.turn, Cells: initialFlippedCells}, &res)
	if err != nil {
		panic(err)
	}
	err = b.distributor.Call(b.callback, stubs.Event{Type: "StateChange", Turn: 0, State: "Executing"}, &res)
	if err != nil {
		panic(err)
	}

	exit := false
	doneWG := sync.WaitGroup{}
	doneWG.Add(1)
	finished := make(chan bool)
	go reportState(finished, &doneWG, b.callback, &b.world, b.distributor, &b.turn)

	// Main game loop
mainLoop:
	for b.turn < b.turns {
		select {
		case pause := <-b.paused:
			// Halt loop and wait until pause is set to false
			for pause {
				pause = <-b.paused
			}
		case <-b.quit:
			// Exit
			exit = true
			break mainLoop
		default:
			nextState(b.width, b.height, b.threads, &b.world, &b.newWorld, b.jobs, &b.wg, &b.flipped)

			superMX.Lock()
			// TODO: move this into helper function
			var f []util.Cell
			for i := 0; i < len(b.world); i++ {
				for j := 0; j < len(b.world[i]); j++ {
					if b.world[i][j] != b.newWorld[i][j] {
						f = append(f, util.Cell{X: j, Y: i})
					}
				}
			}

			deepCopy(&b.world, &b.newWorld)

			err = b.distributor.Call(b.callback, stubs.Event{Type: "CellsFlipped", Turn: b.turn, Cells: f}, &res)
			if err != nil {
				fmt.Println(err)
			}
			err = b.distributor.Call(b.callback, stubs.Event{Type: "TurnComplete", Turn: b.turn}, &res)
			if err != nil {
				fmt.Println(err)
			}
			b.turn++
			superMX.Unlock()
		}
	}
	if !exit {
		superMX.Lock()
		aliveCells := getAliveCells(b.world)
		err = b.distributor.Call(b.callback, stubs.Event{Type: "FinalTurnComplete", Turn: b.turn, Cells: aliveCells}, &res)
		if err != nil {
			fmt.Println(err)
		}
		err = b.distributor.Call(b.callback, stubs.Event{Type: "StateChange", Turn: b.turn, State: "Quitting"}, &res)
		if err != nil {
			fmt.Println(err)
		}
		superMX.Unlock()
	}
	finished <- true
	doneWG.Wait()
	return
}

// Finish Called by distributor to return final world
func (b *Broker) Finish(req stubs.StatusReport, res *stubs.Update) (err error) {
	superMX.Lock()
	res.World = make([][]byte, len(b.world))
	for i := range res.World {
		res.World[i] = make([]byte, len(b.world[i]))
	}
	deepCopy(&res.World, &b.world)
	res.Turn = b.turn
	superMX.Unlock()
	return
}

// Subscribe Called by Server to subscribe to jobs
func (b *Broker) Subscribe(req stubs.Subscription, res *stubs.StatusReport) (err error) {
	client, err := rpc.Dial("tcp", req.FactoryAddress)
	if err == nil {
		go subscriberLoop(client, req.Callback, b.jobs, &b.newWorld, &b.flipped, &b.wg)
	} else {
		fmt.Println("Error subscribing: ", err)
	}
	return
}

func (b *Broker) HandleKey(req stubs.KeyPress, res *stubs.StatusReport) (err error) {
	placeHolder := new(stubs.StatusReport)
	if req.Key == "s" {
		if !req.Paused {
			b.paused <- true
		}
		if !req.Paused {
			b.paused <- false
		}
	} else if req.Key == "q" {
		if !req.Paused {
			b.paused <- true
		}
		superMX.Lock()
		err = b.distributor.Call(b.callback, stubs.Event{Type: "FinalTurnComplete", Turn: b.turn, Cells: getAliveCells(b.world)}, &placeHolder)
		if err != nil {
			fmt.Println(err)
		}
		err = b.distributor.Call(b.callback, stubs.Event{Type: "StateChange", Turn: b.turn, State: "Quitting"}, &placeHolder)
		if err != nil {
			fmt.Println(err)
		}
		superMX.Unlock()
		b.quit <- true
		b.paused <- false

	} else if req.Key == "k" {
		if !req.Paused {
			b.paused <- true
		}
		superMX.Lock()
		err = b.distributor.Call(b.callback, stubs.Event{Type: "FinalTurnComplete", Turn: b.turn, Cells: getAliveCells(b.world)}, &placeHolder)
		if err != nil {
			fmt.Println(err)
		}
		err = b.distributor.Call(b.callback, stubs.Event{Type: "StateChange", Turn: b.turn, State: "Quitting"}, &placeHolder)
		if err != nil {
			fmt.Println(err)
		}
		superMX.Unlock()
		b.quit <- true
		b.paused <- false
		closeWorkers(b.workerAddresses)

	} else if req.Key == "p" {
		if req.Paused {
			superMX.Lock()
			err = b.distributor.Call(b.callback, stubs.Event{Type: "StateChange", Turn: b.turn, State: "Executing"}, &placeHolder)
			if err != nil {
				fmt.Println(err)
			}
			superMX.Unlock()
			b.paused <- false
		} else {
			superMX.Lock()
			res.Message = strconv.Itoa(b.turn)
			err = b.distributor.Call(b.callback, stubs.Event{Type: "StateChange", Turn: b.turn, State: "Paused"}, &placeHolder)
			if err != nil {
				fmt.Println(err)
			}
			superMX.Unlock()
			b.paused <- true
		}
	}
	return
}

func main() {
	pAddr = flag.String("port", "8030", "Port to listen on")
	flag.Parse()
	err := rpc.Register(&Broker{})
	if err != nil {
		fmt.Println("Error: ", err)
	}
	listener, err := net.Listen("tcp", ":"+*pAddr)
	if err != nil {
		fmt.Printf("Error connecting to server port %s", *pAddr)
		listener, err = net.Listen("tcp", ":0") //:0 binds to a random available server port
		if err != nil {
			fmt.Printf("Error: no ports available")
		}
	}
	//potentially tweak the above by iterating through all possible ports around the target port
	//rather than binding to a random one?

	defer listener.Close()
	rpc.Accept(listener)
}

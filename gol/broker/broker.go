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
var workerCount int
var workerAddresses []*exec.Cmd
var height int
var width int
var threads int
var turns int
var callback string
var address string
var distributor *rpc.Client
var jobs = make(chan stubs.Request, 64)
var jobsMX sync.RWMutex
var world [][]byte
var worldMX sync.RWMutex
var newWorld [][]byte
var newWorldMX sync.RWMutex
var superMX sync.RWMutex
var flipped []util.Cell
var flippedMX sync.RWMutex
var wg sync.WaitGroup
var wgMX sync.RWMutex
var pAddr *string
var paused = make(chan bool)
var quit = make(chan bool)
var turn int

// Deep copy one array into another
func deepCopy(output *[][]byte, original *[][]byte) {
	for i := 0; i < len(*original); i++ {
		copy((*output)[i], (*original)[i])
	}
}

// Spawn a new worker, this worker will dial itself into the broker
func spawnWorkers() {
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

func closeWorkers() {
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
func publish() {
	splitRequest := new(stubs.Request)
	worldMX.Lock()
	splitRequest.World, splitRequest.Width, splitRequest.Height = world, width, height
	worldMX.Unlock()
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
func subscriberLoop(client *rpc.Client, callback string) {
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
			newWorld[job.StartY+i] = response.World[i]
		}
		for i := 0; i < len(response.Flipped); i++ {
			flipped = append(flipped, response.Flipped[i])
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

func reportState(finished <-chan bool, wg *sync.WaitGroup) {
	for {
		select {
		case <-finished:
			wg.Done()
			return
		case <-time.After(2 * time.Second):
			superMX.Lock()
			alive := getAliveCells(world) //FIX
			res := new(stubs.StatusReport)
			err := distributor.Call(callback, stubs.Event{Type: "AliveCellsCount", Turn: 0, Count: len(alive)}, &res)
			if err != nil {
				panic(err)
			}
			superMX.Unlock()
		}
	}
}

// increment game loop by one step
func nextState() {
	publish()
	// Wait until all jobs have been processed
	wg.Wait()
	superMX.Lock()
	deepCopy(&world, &newWorld)
	flipped = nil
	superMX.Unlock()
	return
}

// TODO: move global variables into struct
type Broker struct{}

// Start Called by distributor to load initial values
func (b *Broker) Start(input stubs.Input, res *stubs.StatusReport) (err error) {
	superMX.Lock()
	height = input.Height
	width = input.Width
	world = input.World
	//deepCopy(&world, &input.World)
	threads = input.Threads
	turns = input.Turns
	callback = input.Callback
	address = input.Address
	newWorld = make([][]byte, height)
	for i := range newWorld {
		newWorld[i] = make([]byte, width)
	}
	superMX.Unlock()
	// If there are not enough workers currently available, spawn more
	if threads > workerCount {
		for i := 0; i < threads-workerCount; i++ {
			spawnWorkers()
		}
	}

	distributor, err = rpc.Dial("tcp", address)
	if err != nil {
		panic(err)
	}
	res = new(stubs.StatusReport)

	turn = 0

	//Make initial calls
	initialFlippedCells := getAliveCells(world)
	err = distributor.Call(callback, stubs.Event{Type: "CellsFlipped", Turn: turn, Cells: initialFlippedCells}, &res)
	if err != nil {
		panic(err)
	}
	err = distributor.Call(callback, stubs.Event{Type: "StateChange", Turn: 0, State: "Executing"}, &res)
	if err != nil {
		panic(err)
	}

	exit := false
	wg := sync.WaitGroup{}
	wg.Add(1)
	finished := make(chan bool)
	go reportState(finished, &wg)

	// Main game loop
mainLoop:
	for turn < turns {
		select {
		case pause := <-paused:
			// Halt loop and wait until pause is set to false
			for pause {
				pause = <-paused
			}
		case <-quit:
			// Exit
			exit = true
			break mainLoop
		default:
			nextState()

			superMX.Lock()
			// TODO: move this into helper function
			var f []util.Cell
			for i := 0; i < len(world); i++ {
				for j := 0; j < len(world[i]); j++ {
					if world[i][j] != newWorld[i][j] {
						f = append(f, util.Cell{X: j, Y: i})
					}
				}
			}

			deepCopy(&world, &newWorld)

			err = distributor.Call(callback, stubs.Event{Type: "CellsFlipped", Turn: turn, Cells: f}, &res)
			if err != nil {
				panic(err)
			}
			err = distributor.Call(callback, stubs.Event{Type: "TurnComplete", Turn: turn}, &res)
			if err != nil {
				panic(err)
			}
			turn++
			superMX.Unlock()
		}
	}
	if !exit {
		superMX.Lock()
		aliveCells := getAliveCells(world)
		err = distributor.Call(callback, stubs.Event{Type: "FinalTurnComplete", Turn: turn, Cells: aliveCells}, &res)
		if err != nil {
			panic(err)
		}
		err = distributor.Call(callback, stubs.Event{Type: "StateChange", Turn: turn, State: "Quitting"}, &res)
		if err != nil {
			panic(err)
		}
		superMX.Unlock()
	}
	finished <- true
	wg.Wait()
	return
}

// Finish Called by distributor to return final world
func (b *Broker) Finish(req stubs.StatusReport, res *stubs.Update) (err error) {
	superMX.Lock()
	res.World = make([][]byte, len(world))
	for i := range res.World {
		res.World[i] = make([]byte, len(world[i]))
	}
	deepCopy(&res.World, &world)
	res.Turn = turn
	superMX.Unlock()
	return
}

// Subscribe Called by Server to subscribe to jobs
func (b *Broker) Subscribe(req stubs.Subscription, res *stubs.StatusReport) (err error) {
	client, err := rpc.Dial("tcp", req.FactoryAddress)
	if err == nil {
		go subscriberLoop(client, req.Callback)
	} else {
		fmt.Println("Error subscribing: ", err)
	}
	return
}

func (b *Broker) HandleKey(req stubs.KeyPress, res *stubs.StatusReport) (err error) {
	if req.Key == "s" {
		if !req.Paused {
			paused <- true
		}
		if !req.Paused {
			paused <- false
		}
	} else if req.Key == "q" {
		if !req.Paused {
			paused <- true
		}
		superMX.Lock()
		err = distributor.Call(callback, stubs.Event{Type: "FinalTurnComplete", Turn: turn, Cells: getAliveCells(world)}, &res)
		if err != nil {
			panic(err)
		}
		err = distributor.Call(callback, stubs.Event{Type: "StateChange", Turn: turn, State: "Quitting"}, &res)
		if err != nil {
			panic(err)
		}
		superMX.Unlock()
		quit <- true
		paused <- false

	} else if req.Key == "k" {
		if !req.Paused {
			paused <- true
		}
		superMX.Lock()
		err = distributor.Call(callback, stubs.Event{Type: "FinalTurnComplete", Turn: turn, Cells: getAliveCells(world)}, &res)
		if err != nil {
			panic(err)
		}
		err = distributor.Call(callback, stubs.Event{Type: "StateChange", Turn: turn, State: "Quitting"}, &res)
		if err != nil {
			panic(err)
		}
		superMX.Unlock()
		quit <- true
		paused <- false
		closeWorkers()

	} else if req.Key == "p" {
		if req.Paused {
			superMX.Lock()
			err = distributor.Call(callback, stubs.Event{Type: "StateChange", Turn: turn, State: "Executing"}, &res)
			if err != nil {
				panic(err)
			}
			superMX.Unlock()
			paused <- false
		} else {
			superMX.Lock()
			res.Message = strconv.Itoa(turn)
			err = distributor.Call(callback, stubs.Event{Type: "StateChange", Turn: turn, State: "Paused"}, &res)
			if err != nil {
				panic(err)
			}
			superMX.Unlock()
			paused <- true
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

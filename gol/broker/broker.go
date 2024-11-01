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
	"uk.ac.bris.cs/gameoflife/gol"
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
		(*output)[i] = (*original)[i]
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
		newWorldMX.Lock()
		for i := 0; i < len(response.World); i++ {

			newWorld[job.StartY+i] = response.World[i]

		}
		newWorldMX.Unlock()
		flippedMX.Lock()
		for i := 0; i < len(response.Flipped); i++ {

			flipped = append(flipped, response.Flipped[i])
		}
		flippedMX.Unlock()
		wgMX.Lock()
		wg.Done()
		wgMX.Unlock()
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
			distributor.Call(callback, gol.AliveCellsCount{turn, len(alive)}, &res)
			superMX.Unlock()
		}
	}
}

// increment game loop by one step
func nextState() {
	publish()
	// Wait until all jobs have been processed
	wg.Wait()
	worldMX.Lock()
	newWorldMX.Lock()
	flippedMX.Lock()
	deepCopy(&world, &newWorld)
	world = newWorld
	//res.Flipped = flipped
	flipped = nil
	flippedMX.Unlock()
	newWorldMX.Unlock()
	worldMX.Unlock()
	return
}

// TODO: move global variables into struct
type Broker struct{}

// Start Called by distributor to load initial values
func (b *Broker) Start(input stubs.Input, res *stubs.StatusReport) (err error) {
	height = input.Height
	width = input.Width
	world = input.World
	threads = input.Threads
	turns = input.Turns
	callback = input.Callback
	address = input.Address
	newWorld = make([][]byte, height)
	for i := range newWorld {
		newWorld[i] = make([]byte, width)
	}
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

	turn := 0

	//Make initial calls
	initialFlippedCells := getAliveCells(world)
	distributor.Call(callback, gol.CellsFlipped{turn, initialFlippedCells}, &res)
	distributor.Call(callback, gol.StateChange{0, gol.Executing}, &res)

	exit := false
	wg := sync.WaitGroup{}
	wg.Add(1)
	finishedR := make(chan bool)
	go reportState(finishedR, &wg)

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
			superMX.Lock()
			nextState()

			// TODO: move this into helper function and remove flipped cell computation from broker?
			var f []util.Cell
			for i := 0; i < len(world); i++ {
				for j := 0; j < len(world[i]); j++ {
					if world[i][j] != newWorld[i][j] {
						f = append(f, util.Cell{j, i})
					}
				}
			}

			world = newWorld

			distributor.Call(callback, gol.CellsFlipped{turn, f}, &res)
			distributor.Call(callback, gol.TurnComplete{turn}, &res)
			turn++

			superMX.Unlock()

		}
	}
	if !exit {
		superMX.Lock()
		aliveCells := getAliveCells(world)
		distributor.Call(callback, gol.FinalTurnComplete{turn, aliveCells}, &res)
		distributor.Call(callback, gol.StateChange{turn, gol.Quitting}, &res)
		superMX.Unlock()
	}
	finishedR <- true
	wg.Wait()
	return
}

// Finish Called by distributor to return final world
func (b *Broker) Finish(req stubs.StatusReport, res *stubs.Update) (err error) {
	worldMX.Lock()
	res.World = make([][]byte, len(world))
	for i := range res.World {
		res.World[i] = make([]byte, len(world[i]))
	}
	deepCopy(&res.World, &world)
	worldMX.Unlock()
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
		superMX.Lock()
		superMX.Unlock()
		if !req.Paused {
			paused <- false
		}
	} else if req.Key == "q" {
		if !req.Paused {
			paused <- true
		}
		superMX.Lock()
		distributor.Call(callback, gol.FinalTurnComplete{turn, getAliveCells(world)}, *res)
		distributor.Call(callback, gol.StateChange{turn, gol.Quitting}, *res)
		superMX.Unlock()
		quit <- true
		paused <- false

	} else if req.Key == "k" {
		if !req.Paused {
			paused <- true
		}
		superMX.Lock()
		distributor.Call(callback, gol.FinalTurnComplete{turn, getAliveCells(world)}, *res)
		distributor.Call(callback, gol.StateChange{turn, gol.Quitting}, *res)
		superMX.Unlock()
		quit <- true
		paused <- false
		closeWorkers()

	} else if req.Key == "p" {
		if req.Paused {
			superMX.Lock()
			distributor.Call(callback, gol.StateChange{turn, gol.Executing}, *res)
			superMX.Unlock()
			paused <- false
		} else {
			superMX.Lock()
			distributor.Call(callback, gol.StateChange{turn, gol.Paused}, *res)
			res.Message = strconv.Itoa(turn)
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

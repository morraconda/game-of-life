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
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

//TODO: optimise mutex locks to reduce waiting time (ie: separate stateMX into a few independent locks)
var stateMX sync.RWMutex
var jobsMX sync.RWMutex
var wgMX sync.RWMutex
var pAddr *string

var workerAddresses = make(map[int]*exec.Cmd)
var workerCount int
var jobs = make(chan stubs.Request, 64)

// Helper: deep copy one array into another
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
	workerAddresses[workerCount] = cmd
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
			fmt.Println("Error closing worker: ", err)
		}
	}
	os.Exit(0)
	return
}

// Receive a board and slice it up into jobs
func publish(width int, height int, threads int, world *[][]byte, wg *sync.WaitGroup) {
	splitRequest := new(stubs.Request)
	stateMX.Lock()
	splitRequest.World = *world
	splitRequest.Width, splitRequest.Height = width, height
	stateMX.Unlock()
	incrementY := height / threads
	startY := 0
	jobsMX.Lock()
	wgMX.Lock()
	fmt.Println(threads)
	for i := 0; i < threads; i++ {
		splitRequest.StartY = startY
		if i == threads-1 {
			splitRequest.EndY = height
		} else {
			splitRequest.EndY = incrementY + startY
		}
		startY += incrementY

		jobs <- *splitRequest
		wg.Add(1)
	}
	wgMX.Unlock()
	jobsMX.Unlock()
}

// Routine ran once per server instance, takes jobs from the queue and sends them to the server
func subscriberLoop(client *rpc.Client, callback string, newWorld *[][]byte, wg *sync.WaitGroup) {
	for {
		//Take a job from the job queue
		job := <-jobs
		response := new(stubs.Response) // Empty response
		job.Routines = 4
		err := client.Call(callback, job, response)
		if err != nil {
			jobs <- job
			fmt.Println("Worker dead, starting new worker:", err)
			delete(workerAddresses, workerCount)
			workerCount--
			spawnWorkers()
			return
		}
		//Append the results to the new state
		stateMX.Lock()
		for i := 0; i < len(response.World); i++ {
			(*newWorld)[job.StartY+i] = response.World[i]
		}
		stateMX.Unlock()
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

// increment game loop by one step
func nextState(width int, height int, threads int, world *[][]byte, newWorld *[][]byte, wg *sync.WaitGroup) {
	publish(width, height, threads, world, wg)
	// Wait until all jobs have been processed
	wg.Wait()
	stateMX.Lock()
	deepCopy(world, newWorld)
	stateMX.Unlock()
	return
}

type Broker struct {
	// Configuration Variables
	height   int
	width    int
	turns    int
	threads  int
	routines int

	// Worker and Job Management
	distributor *rpc.Client
	wg          sync.WaitGroup

	// State Variables
	world    [][]byte // Current world
	newWorld [][]byte // New world
	oldWorld [][]byte // World from last update request
	pause    chan bool
	paused   bool
	quit     chan bool
	done     chan bool
	turn     int
	reset    bool
}

func (b *Broker) Init(input stubs.Input, res *stubs.StatusReport) (err error) {
	// Transfer and initialise variables
	stateMX.Lock()
	b.quit = make(chan bool)
	b.pause = make(chan bool)
	b.done = make(chan bool)
	b.height = input.Height
	b.width = input.Width
	b.world = input.World
	b.threads = input.Threads
	b.routines = input.Routines
	b.turns = input.Turns
	b.newWorld = make([][]byte, b.height)
	b.oldWorld = make([][]byte, b.height)
	b.paused = false
	b.turn = 0
	b.reset = false
	for i := range b.newWorld {
		b.newWorld[i] = make([]byte, b.width)
		b.oldWorld[i] = make([]byte, b.width)
	}
	stateMX.Unlock()

	// If there are not enough workers currently available, spawn more
	if b.threads > workerCount {
		for i := 0; i < b.threads-workerCount; i++ {
			spawnWorkers()
		}
	}
	return err
}

// Start Called by distributor to begin main game loop
func (b *Broker) Start(req stubs.StatusReport, res *stubs.StatusReport) (err error) {
	// Main game loop
mainLoop:
	for b.turn < b.turns {
		select {
		case pause := <-b.pause:
			b.paused = true
			// Halt loop and wait until pause is set to false
			for pause {
				select {
				case pause = <-b.pause:
				case <-b.quit:
					break mainLoop
				}

			}
			b.paused = false
		case <-b.quit:
			// Exit
			break mainLoop
		default:
			// Increment state
			nextState(b.width, b.height, b.threads, &b.world, &b.newWorld, &b.wg)
			stateMX.Lock()
			// Overwrite old world
			deepCopy(&b.world, &b.newWorld)
			b.turn++
			stateMX.Unlock()
		}
	}
	b.reset = true
	b.done <- true
	return err
}

// Executing Used to block the distributor when game is still executing
func (b *Broker) Executing(req stubs.StatusReport, res *stubs.StatusReport) (err error) {
	<-b.done
	return err
}

func (b *Broker) Pause(req stubs.PauseData, res *stubs.PauseData) (err error) {
	if req.Value == 1 {
		b.pause <- true
	} else if req.Value == 0 {
		b.pause <- false
	}
	res.Value = b.turn
	return
}

func (b *Broker) Quit(req stubs.PauseData, res *stubs.PauseData) (err error) {
	b.quit <- true
	b.reset = true
	return
}

func (b *Broker) ShutDown(req stubs.PauseData, res *stubs.PauseData) (err error) {
	b.quit <- true
	closeWorkers()
	return
}

// Subscribe Called by Server to subscribe to jobs
func (b *Broker) Subscribe(req stubs.Subscription, res *stubs.StatusReport) (err error) {
	client, err := rpc.Dial("tcp", req.WorkerAddress)
	if err == nil {
		fmt.Println("Worker connected from: ", req.WorkerAddress)
		go subscriberLoop(client, req.Callback, &b.newWorld, &b.wg)
	} else {
		fmt.Println("Lost connection with spawned worker, spawning another: ", err)
		delete(workerAddresses, workerCount)
		workerCount--
		spawnWorkers()
	}
	return
}

// GetTotalFlipped returns the cells flipped from game start to the current state
func (b *Broker) GetTotalFlipped(req stubs.StatusReport, res *stubs.Update) (err error) {
	stateMX.Lock()
	for i := 0; i < len(b.world); i++ {
		for j := 0; j < len(b.world[i]); j++ {
			if b.world[i][j] == 255 {
				res.Flipped = append(res.Flipped, util.Cell{X: j, Y: i})
			}
		}
	}
	res.Turn = b.turn
	stateMX.Unlock()
	return
}

func (b *Broker) GetState(req stubs.StatusReport, res *stubs.Update) (err error) {
	stateMX.Lock()
	res.Paused = b.paused
	if b.reset {
		res.Running = false
		res.Paused = false
	} else if b.turn == 0 {
		res.Running = false
		res.Paused = false
	} else {
		res.Running = true
	}
	res.World = make([][]byte, b.height)
	for i := range res.World {
		res.World[i] = make([]byte, b.width)
	}
	deepCopy(&res.World, &b.world)
	res.World = b.world
	res.Turn = b.turn
	res.AliveCells = getAliveCells(b.world)
	for i := 0; i < len(b.world); i++ {
		for j := 0; j < len(b.world[i]); j++ {
			if b.world[i][j] != b.oldWorld[i][j] {
				res.Flipped = append(res.Flipped, util.Cell{X: j, Y: i})
			}
		}
	}
	deepCopy(&b.oldWorld, &b.world)
	stateMX.Unlock()
	return err
}

func main() {
	pAddr = flag.String("port", "8030", "Port to listen on")
	flag.Parse()
	err := rpc.Register(&Broker{})
	if err != nil {
		fmt.Println("Failed to register RPC methods: ", err)
	}
	listener, err := net.Listen("tcp", ":"+*pAddr)
	if err != nil {
		fmt.Printf("Error connecting to server port %s", *pAddr)
		listener, err = net.Listen("tcp", ":0") //:0 binds to a random available server port
		if err != nil {
			fmt.Printf("Error: no ports available")
		}
	}

	defer listener.Close()
	rpc.Accept(listener)
}

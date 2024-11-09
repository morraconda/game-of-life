package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

//TODO: optimise mutex locks to reduce waiting time (ie: separate superMX into a few independent locks)
var superMX sync.RWMutex
var jobsMX sync.RWMutex
var wgMX sync.RWMutex
var pAddr *string
var workerAddresses []*exec.Cmd
var workerAddressStrings []string
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
		log.Fatalf("Error listening for worker address: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	// Start a worker at this address
	cmd := exec.Command("go", "run", "../server/server.go", "-ip=127.0.0.1:"+strconv.Itoa(port), "-broker=127.0.0.1:"+*pAddr)
	err = cmd.Start()
	if err != nil {
		log.Fatalf("Error starting worker process: %v", err)
	}

	// Append worker details only if successful
	workerAddresses = append(workerAddresses, cmd)
	workerAddressStrings = append(workerAddressStrings, "-ip=127.0.0.1:"+strconv.Itoa(port))
	workerCount++
	log.Printf("Spawned worker at 127.0.0.1:%d", port)
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
	superMX.Lock()
	splitRequest.World = *world
	splitRequest.Width, splitRequest.Height = width, height
	superMX.Unlock()
	incrementY := height / threads
	startY := 0
	jobsMX.Lock()
	wgMX.Lock()

	//make sure there are enough workers
	for i := len(workerAddressStrings); i < threads; i++ {
		spawnWorkers()
	}

	//debugging: printing workeraddress strings to see if neighbours were assigned right
	for p, v := range workerAddressStrings {
		fmt.Println(p, v)
	}

	//setting the board into jobs
	for i := 0; i < threads; i++ {
		splitRequest.StartY = startY
		if i == threads-1 {
			splitRequest.EndY = height
		} else {
			splitRequest.EndY = incrementY + startY
		}
		startY += incrementY

		// Debugging: Print the assigned slice for this job
		fmt.Printf("Job %d assigned rows from %d to %d\n", i, splitRequest.StartY, splitRequest.EndY)
		//error message if the workerAddressStrings slice isnt equal to threads
		if len(workerAddressStrings) != threads {
			log.Fatalf("Mismatch between workerAddressStrings length (%d) and threads (%d)", len(workerAddressStrings), threads)
		}

		// Assign neighbors THERE IS SOMETHING WRONG WITH HOW THEY ARE ASSIGNED
		splitRequest.TopNeighbor = workerAddressStrings[(i+threads)%threads]

		splitRequest.BottomNeighbor = workerAddressStrings[(i+1)%threads]

		// Debugging: Print neighbor assignment for this job
		fmt.Printf("Job %d has TopNeighbor: %s and BottomNeighbor: %s\n", i, splitRequest.TopNeighbor, splitRequest.BottomNeighbor)

		// Add job to the channel
		jobs <- *splitRequest
		wg.Add(1)
	}

	// Debugging: Confirm all jobs are published
	fmt.Printf("Published %d jobs\n", threads)

	wgMX.Unlock()
	jobsMX.Unlock()
}

// Routine ran once per server instance, takes jobs from the queue and sends them to the server
func subscriberLoop(client *rpc.Client, callback string, newWorld *[][]byte, wg *sync.WaitGroup) {
	for {
		//Take a job from the job queue
		job := <-jobs
		response := new(stubs.Response) // Empty response

		log.Printf("Sending job to worker via RPC:")
		err := client.Call(callback, job, response)
		if err != nil {
			log.Printf("Error in RPC call: %v", err)
			return
		}

		//Append the results to the new state
		superMX.Lock()
		for i := 0; i < len(response.World); i++ {
			(*newWorld)[job.StartY+i] = response.World[i]
		}

		superMX.Unlock()
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
	superMX.Lock()
	deepCopy(world, newWorld)
	superMX.Unlock()
	return
}

type Broker struct {
	// Configuration Variables
	height  int
	width   int
	turns   int
	threads int

	// Worker and Job Management
	distributor *rpc.Client
	wg          sync.WaitGroup

	// State Variables
	world    [][]byte
	newWorld [][]byte
	oldWorld [][]byte
	paused   chan bool
	quit     chan bool
	turn     int
}

func (b *Broker) Init(input stubs.Input, res *stubs.StatusReport) (err error) {
	// Transfer and initialise variables
	b.quit = make(chan bool)
	b.paused = make(chan bool)
	superMX.Lock()
	b.height = input.Height
	b.width = input.Width
	b.world = input.World
	b.threads = input.Threads
	b.turns = input.Turns
	b.newWorld = make([][]byte, b.height)
	b.oldWorld = make([][]byte, b.height)
	b.turn = 0
	for i := range b.newWorld {
		b.newWorld[i] = make([]byte, b.width)
		b.oldWorld[i] = make([]byte, b.width)
	}
	superMX.Unlock()

	// If there are not enough workers currently available, spawn more
	if b.threads > workerCount {
		for i := 0; i < b.threads-workerCount; i++ {
			spawnWorkers()
		}
	}
	return err
}

// Start Called by distributor
func (b *Broker) Start(req stubs.StatusReport, res *stubs.StatusReport) (err error) {
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
			break mainLoop
		default:
			// Increment state
			nextState(b.width, b.height, b.threads, &b.world, &b.newWorld, &b.wg)
			superMX.Lock()
			// Overwrite old world
			deepCopy(&b.world, &b.newWorld)
			b.turn++
			superMX.Unlock()
		}
	}
	return err
}

func (b *Broker) Pause(req stubs.PauseData, res *stubs.PauseData) (err error) {
	if req.Value == 1 {
		b.paused <- true
	} else if req.Value == 0 {
		b.paused <- false
	}
	res.Value = b.turn
	return
}

func (b *Broker) Quit(req stubs.PauseData, res *stubs.PauseData) (err error) {
	b.quit <- true
	return
}

func (b *Broker) ShutDown(req stubs.PauseData, res *stubs.PauseData) (err error) {
	b.quit <- true
	closeWorkers()
	return
}

// Subscribe Called by Server to subscribe to jobs
func (b *Broker) Subscribe(req stubs.Subscription, res *stubs.StatusReport) (err error) {
	client, err := rpc.Dial("tcp", req.FactoryAddress)
	if err == nil {
		go subscriberLoop(client, req.Callback, &b.newWorld, &b.wg)
	} else {
		fmt.Println("Lost connection with spawned worker, spawning another: ", err)
		spawnWorkers()
	}
	return
}

func (b *Broker) GetState(req stubs.StatusReport, res *stubs.Update) (err error) {
	superMX.Lock()
	if len(b.world) == 0 || len(b.world[0]) == 0 {
		fmt.Println("Error: Broker world not initialized")
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
	superMX.Unlock()
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

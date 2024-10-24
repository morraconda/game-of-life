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

// Public variables
var workerCount int
var workerAddresses []*exec.Cmd
var height int
var width int
var threads int
var jobs = make(chan stubs.Request, 64)
var jobsMX sync.RWMutex
var world [][]byte
var worldMX sync.RWMutex
var newWorld [][]byte
var newWorldMX sync.RWMutex
var flipped []util.Cell
var flippedMX sync.RWMutex
var wg sync.WaitGroup
var wgMX sync.RWMutex
var pAddr *string

// Deep copy one array into another
func deepCopy(output *[][]byte, original *[][]byte) {
	for i := 0; i < len(*original); i++ {
		(*output)[i] = (*original)[i]
	}
}

// Spawn a new worker, this worker will dial iteself into the broker
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

// Receive a board and slice it up into jobs
func publish() {
	splitRequest := new(stubs.Request)
	splitRequest.World, splitRequest.Width, splitRequest.Height = world, width, height
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
			fmt.Println("Error calling: ", err)
			jobsMX.Lock()
			jobs <- job
			jobsMX.Unlock()
		}
		//Append the results to the new state
		for i := 0; i < len(response.World); i++ {
			newWorldMX.Lock()
			flippedMX.Lock()
			newWorld[job.StartY+i] = response.World[i]
			flippedMX.Unlock()
			newWorldMX.Unlock()
		}
		flipped = append(flipped, response.Flipped...)
		wgMX.Lock()
		wg.Done()
		wgMX.Unlock()
	}
}

type Broker struct{}

// NextState Called by distributor to increment the game by one step
func (b *Broker) NextState(req stubs.StatusReport, res *stubs.Update) (err error) {
	publish()
	// Wait until all jobs have been processed
	wg.Wait()
	worldMX.Lock()
	newWorldMX.Lock()
	deepCopy(&world, &newWorld)
	newWorldMX.Unlock()
	worldMX.Unlock()
	res.Flipped = flipped
	flipped = nil
	return
}

// Start Called by distributor to load initial values
func (b *Broker) Start(input stubs.Input, res *stubs.StatusReport) (err error) {
	height = input.Height
	width = input.Width
	world = input.World
	threads = input.Threads
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
	return
}

func (b *Broker) Close(req stubs.StatusReport, res *stubs.StatusReport) (err error) {
	for _, worker := range workerAddresses {
		err := worker.Process.Kill()
		if err != nil {
			fmt.Println("Error: ", err)
		}
	}
	os.Exit(0)
	return
}

// Finish Called by distributor to return final world
func (b *Broker) Finish(req stubs.StatusReport, res *stubs.Output) (err error) {
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

func main() {
	// TODO: Make this dynamic in case the address is not available
	pAddr = flag.String("port", "8030", "Port to listen on")
	flag.Parse()
	err := rpc.Register(&Broker{})
	if err != nil {
		fmt.Println("Error: ", err)
	}
	listener, _ := net.Listen("tcp", ":"+*pAddr)
	defer listener.Close()
	rpc.Accept(listener)
}

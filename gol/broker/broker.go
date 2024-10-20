package main

import (
	"flag"
	"fmt"
	"net"
	"net/rpc"
	"sync"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

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

func deepCopy(output *[][]byte, original *[][]byte) {
	for i := 0; i < len(*original); i++ {
		(*output)[i] = (*original)[i]
	}
}

func nextState(res *stubs.Update) {
	publish()
	wg.Wait()
	deepCopy(&world, &newWorld)
	res.Flipped = flipped
	flipped = nil
}

// Receive a board and slice it up into jobs
func publish() {
	splitRequest := new(stubs.Request)
	splitRequest.World = world
	splitRequest.Width = width
	splitRequest.Height = height
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

func subscriberLoop(client *rpc.Client, callback string) {
	for {
		//Take a job from the job queue
		job := <-jobs
		response := new(stubs.Response)
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
			flipped = append(flipped, response.Flipped...)
			flippedMX.Unlock()
			newWorldMX.Unlock()
		}
		wgMX.Lock()
		wg.Done()
		wgMX.Unlock()
	}
}

func subscribe(req stubs.Subscription) {
	client, err := rpc.Dial("tcp", req.FactoryAddress)
	if err == nil {
		go subscriberLoop(client, req.Callback)
	} else {
		fmt.Println("Error subscribing: ", err)
	}
	return
}

type Broker struct{}

func (b *Broker) NextState(req stubs.StatusReport, res *stubs.Update) (err error) {
	nextState(res)
	return
}

func (b *Broker) Start(input stubs.Input, res *stubs.StatusReport) (err error) {
	height = input.Height
	width = input.Width
	world = input.World
	threads = input.Threads
	newWorld = make([][]byte, height)
	for i := range newWorld {
		newWorld[i] = make([]byte, width)
	}
	return
}

func (b *Broker) Finish(req stubs.StatusReport, res *stubs.Output) (err error) {
	res.World = make([][]byte, len(world))
	for i := range res.World {
		res.World[i] = make([]byte, len(world[i])) // Make sure each row has the correct width
	}
	deepCopy(&res.World, &world)
	return
}

func (b *Broker) Subscribe(req stubs.Subscription, res *stubs.StatusReport) (err error) {
	fmt.Println("Subscription request from", req.FactoryAddress)
	subscribe(req)
	return
}

func main() {
	pAddr := flag.String("port", "8030", "Port to listen on")
	flag.Parse()
	err := rpc.Register(&Broker{})
	if err != nil {
		fmt.Println("Error: ", err)
	}
	listener, _ := net.Listen("tcp", ":"+*pAddr)
	defer listener.Close()
	rpc.Accept(listener)
}

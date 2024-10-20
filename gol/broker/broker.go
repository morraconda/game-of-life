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

func nextState(res *stubs.Update) {
	publish()
	wg.Wait()
	world = newWorld
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
			jobs <- job
		}

		//Append the results to the new state
		for i := 0; i < len(newWorld); i++ {
			newWorldMX.Lock()
			flippedMX.Lock()
			newWorld[job.StartY+i] = newWorld[i]
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
	fmt.Println("New job")
	worldMX.Lock()
	height = input.Height
	width = input.Width
	world = input.World
	worldMX.Unlock()
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
	rpc.Register(&Broker{})
	listener, _ := net.Listen("tcp", ":"+*pAddr)
	defer listener.Close()
	rpc.Accept(listener)
}

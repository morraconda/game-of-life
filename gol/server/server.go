package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"os"
	"sync"
	"time"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

var listener net.Listener
var f *os.File

// returns in-bounds version of a cell if out of bounds
func wrap(cell util.Cell, width int, height int) util.Cell {
	if cell.X == -1 {
		cell.X = width - 1
	} else if cell.X == width {
		cell.X = 0
	}

	if cell.Y == -1 {
		cell.Y = height - 1
	} else if cell.Y == height {
		cell.Y = 0
	}
	return cell
}

//there is already a wrap function - use it!

// returns list of cells adjacent to the one given, accounting for wraparounds
func getAdjacentCells(cell util.Cell, width int, height int) []util.Cell {
	var adjacent []util.Cell
	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if !(i == 0 && j == 0) {
				next := util.Cell{X: cell.X + j, Y: cell.Y + i}
				next = wrap(next, width, height)
				adjacent = append(adjacent, next)
			}
		}
	}
	return adjacent
}

// return how many cells in a list are black
func countAdjacentCells(current util.Cell, world [][]byte, width int, height int) int {
	count := 0
	cells := getAdjacentCells(current, width, height)
	for _, cell := range cells {
		//count += world[cell.Y][cell.X] / 255
		if world[cell.Y][cell.X] == 255 {
			count++
		}
	}
	return count
}

// get next value of a cell, given a world
func getNextCell(cell util.Cell, world [][]byte, width int, height int) byte {
	neighbours := countAdjacentCells(cell, world, width, height)
	if neighbours == 3 {
		return 255
	} else if neighbours == 2 {
		return world[cell.Y][cell.X]
	} else {
		return 0
	}
}

//THESE TWO FUNCTIONS ARE NOT NEEDED IN THE NON HALOEXCHANGE IMPLEMENTATION
// sending rows to neighbour via rpc address dialing
func sendTopRow(topRow []byte, neighborAddr string) error {
	var haloRes stubs.HaloResponse
	client, err := rpc.Dial("tcp", neighborAddr)
	if err != nil {
		return fmt.Errorf("error dialing top neighbor %s: %w", neighborAddr, err)
	}
	defer client.Close()
	return client.Call("Compute.GetBottomRow", stubs.HaloRequest{Row: topRow}, &haloRes)
}

func sendBottomRow(bottomRow []byte, neighborAddr string) error {
	var haloRes stubs.HaloResponse
	client, err := rpc.Dial("tcp", neighborAddr)
	if err != nil {
		return fmt.Errorf("error dialing bottom neighbor %s: %w", neighborAddr, err)
	}
	defer client.Close()
	return client.Call("Compute.GetTopRow", stubs.HaloRequest{Row: bottomRow}, &haloRes)
}

type Compute struct {
	topRowChan    chan []byte
	bottomRowChan chan []byte
	topRow        []byte
	bottomRow     []byte
}

// GetTopRow functions for getting the halo region top and bottom rows from the halo request
func (s *Compute) GetTopRow(haloReq stubs.HaloRequest, haloRes *stubs.HaloResponse) {
	s.topRowChan <- haloReq.Row
	haloRes.Received = true
}

// GetBottomRow get the bottom row from a neighbour
func (s *Compute) GetBottomRow(haloReq stubs.HaloRequest, haloRes *stubs.HaloResponse) {
	s.bottomRowChan <- haloReq.Row
	haloRes.Received = true
}

func (s *Compute) SimulateTurn(req stubs.Request, res *stubs.Response) error {
	s.topRowChan = make(chan []byte, 1)
	s.bottomRowChan = make(chan []byte, 1)

	// Define the top and bottom rows of the section to be sent to neighbors
	topRow := req.World[req.StartY]
	bottomRow := req.World[req.EndY-1]

	// Send rows to neighbors with error handling and debugging
	log.Printf("Sending top row to neighbor at %s", req.TopNeighbor)
	if err := sendTopRow(topRow, req.TopNeighbor); err != nil {
		log.Printf("Error sending top row to %s: %v", req.TopNeighbor, err)
		return err
	}

	log.Printf("Sending bottom row to neighbor at %s", req.BottomNeighbor)
	if err := sendBottomRow(bottomRow, req.BottomNeighbor); err != nil {
		log.Printf("Error sending bottom row to %s: %v", req.BottomNeighbor, err)
		return err
	}

	// Wait for halo rows to be received with extended timeout and debug output
	select {
	case s.topRow = <-s.topRowChan:
		log.Println("Top row received successfully.")
	case <-time.After(10 * time.Second): // Increased from 5 to 10 seconds
		log.Printf("Timed out waiting for top row from neighbor %s", req.TopNeighbor)
		return fmt.Errorf("timed out waiting for top row from neighbor %s", req.TopNeighbor)
	}

	select {
	case s.bottomRow = <-s.bottomRowChan:
		log.Println("Bottom row received successfully.")
	case <-time.After(10 * time.Second): // Increased from 5 to 10 seconds
		log.Printf("Timed out waiting for bottom row from neighbor %s", req.BottomNeighbor)
		return fmt.Errorf("timed out waiting for bottom row from neighbor %s", req.BottomNeighbor)
	}

	// Prepare the temporary world with halo rows
	tempWorld := make([][]byte, req.EndY-req.StartY+2)
	tempWorld[0] = s.topRow
	for i := 0; i < req.EndY-req.StartY; i++ {
		tempWorld[i+1] = req.World[i+req.StartY]
	}
	tempWorld[req.EndY-req.StartY+1] = s.bottomRow

	newWorld := make([][]byte, req.EndY-req.StartY)
	var flipped []util.Cell

	// Process each cell in the segment, excluding halo rows
	for i := 1; i < len(tempWorld)-1; i++ {
		newWorld[i-1] = make([]byte, req.Width)
		for j := 0; j < req.Width; j++ {
			cell := util.Cell{X: j, Y: i - 1 + req.StartY}
			old := tempWorld[i][j]
			next := getNextCell(cell, tempWorld, req.Width, req.Height)

			if old != next {
				flipped = append(flipped, cell)
			}
			newWorld[i-1][j] = next
		}
	}

	res.World = newWorld
	res.Flipped = flipped
	log.Printf("Job processed successfully")
	return nil
}

func main() {
	err := *new(error)
	f, err = os.OpenFile("testlogfile", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening log: %v", err)
	}
	log.SetOutput(f)

	pAddr := flag.String("ip", "127.0.0.1:8050", "IP and port to listen on")
	brokerAddr := flag.String("broker", "127.0.0.1:8030", "Address of broker instance")
	flag.Parse()
	log.Printf("Worker started at: %s", *pAddr)

	err = rpc.Register(&Compute{})
	if err != nil {
		log.Fatalf("Failed to register Compute service: %v", err)
	}

	//Start listener
	go func() {
		listener, err = net.Listen("tcp", *pAddr)
		if err != nil {
			log.Fatalf("Failed to listen on %s: %v", *pAddr, err)
		}
		defer listener.Close()
		rpc.Accept(listener)
	}()

	//Dial the broker
	time.Sleep(500 * time.Millisecond)
	brokerClient, err := rpc.Dial("tcp", *brokerAddr)
	if err != nil {
		log.Fatalf("Failed to connect to broker at %s: %v", *brokerAddr, err)
	}
	defer brokerClient.Close()

	//subscribe to jobs
	subscription := stubs.Subscription{FactoryAddress: *pAddr, Callback: "Compute.SimulateTurn"}
	var subscriptionRes stubs.StatusReport
	err = brokerClient.Call("Broker.Subscribe", subscription, &subscriptionRes)
	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}

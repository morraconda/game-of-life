package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/rpc"
	"os"
	"sync"
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

//WTHE POINT OF HALOEXCHANGE IS TO EXCHANGE INFORMATION BETWEEN THE CELLS IN ADJACENT ROWS BUT DIFFERENT WORKERS BECAUSE NEXT STATES ARE DEPENDENT ON ADJACENT CELLS
//SEE MEDIAN FILTER MAYBE FOR AN EXAMPLE ON HOW TO IMPLEMENT

//sending rows to neighbour via rpc address dialing
func sendTopRow(topRow []byte, neighborAddr string) error {
	var haloRes stubs.HaloResponse
	client, err := rpc.Dial("tcp", neighborAddr)
	if err != nil {
		fmt.Println("Error sending row", err)
	}
	defer client.Close()
	haloReq := stubs.HaloRequest{Row: topRow}
	return client.Call("Compute.GetBottomRow", haloReq, &haloRes)
}

func sendBottomRow(bottomRow []byte, neighborAddr string) error {
	var haloRes stubs.HaloResponse
	client, err := rpc.Dial("tcp", neighborAddr)
	if err != nil {
		fmt.Println("Error sending row", err)
	}
	defer client.Close()
	haloReq := stubs.HaloRequest{Row: bottomRow}
	return client.Call("Compute.GetTopRow", haloReq, &haloRes)
}

type Compute struct {
	topRow    []byte
	bottomRow []byte //halo rows?
	wg        sync.WaitGroup
}

//functions for getting the halo region top and bottom rows from the halo request
func (s *Compute) GetTopRow(haloReq stubs.HaloRequest, haloRes stubs.HaloResponse) {
	s.topRow = haloReq.Row
	haloRes.Received = true
	s.wg.Done()
	return
}

// GetBottomRow get the bottom row from a neighbour
func (s *Compute) GetBottomRow(haloReq stubs.HaloRequest, haloRes stubs.HaloResponse) {
	s.bottomRow = haloReq.Row
	haloRes.Received = true
	s.wg.Done()
	return
}

func (s *Compute) SimulateTurn(req stubs.Request, res *stubs.Response) error {
	s.wg.Add(2)
	// Define the top and bottom rows of the section to be sent to neighbors
	topRow := req.World[req.StartY]
	bottomRow := req.World[req.EndY-1]

	// Send rows to neighbors concurrently
	go sendTopRow(topRow, req.TopNeighbor)
	go sendBottomRow(bottomRow, req.BottomNeighbor)

	s.wg.Wait()

	// Create a temporary world slice with additional space for halo rows
	tempWorld := make([][]byte, req.EndY-req.StartY+2)
	tempWorld[0] = s.topRow // Add the received top halo row
	for i := req.StartY; i < req.EndY; i++ {
		tempWorld[i-req.StartY+1] = req.World[i] // Copy middle rows
	}
	tempWorld[req.EndY-req.StartY+1] = s.bottomRow // Add the received bottom halo row

	// Initialize newWorld to store the evolved world section
	newWorld := make([][]byte, req.EndY-req.StartY)
	var flipped []util.Cell // Slice of cells which have been flipped

	// Process each cell in the current segment, excluding halo rows
	for i := 1; i < len(tempWorld)-1; i++ {
		newWorld[i-1] = make([]byte, req.Width)
		for j := 0; j < req.Width; j++ {
			// Check and update cell state
			cell := util.Cell{X: j, Y: i - 1 + req.StartY} // Original position in the full world
			old := tempWorld[i][j]
			next := getNextCell(cell, tempWorld, req.Width, req.Height) // Determine next state

			if old != next {
				flipped = append(flipped, cell)
			}
			newWorld[i-1][j] = next
		}
	}

	//newWorld := make([][]byte, req.EndY-req.StartY)
	//var flipped []util.Cell
	//for i := req.StartY; i < req.EndY; i++ {
	//	// initialise row, set the contents of the row accordingly
	//	newWorld[i-req.StartY] = make([]byte, req.Width)
	//	for j := 0; j < req.Width; j++ {
	//		// check for flipped cells
	//		cell := util.Cell{X: j, Y: i}
	//		old := newWorld[i-req.StartY][j]
	//		next := getNextCell(cell, req.World, req.Width, req.Height)
	//		if old != next {
	//			flipped = append(flipped, cell)
	//		}
	//		newWorld[i-req.StartY][j] = next
	//	}
	//}

	res.World = newWorld
	res.Flipped = flipped
	log.Printf("Job processed")
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
	brokerClient, err := rpc.Dial("tcp", *brokerAddr)
	if err != nil {
		log.Fatalf("Failed to connect to broker at %s: %v", *brokerAddr, err)
	}
	defer brokerClient.Close()

	//subscribe to jobs
	subscription := stubs.Subscription{FactoryAddress: *pAddr, Callback: "Compute.SimulateTurn"}
	err = brokerClient.Call(stubs.Subscribe, subscription, 'a')

	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}

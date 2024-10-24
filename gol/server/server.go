package main

import (
	"flag"
	"log"
	"net"
	"net/rpc"
	"os"
	"sync"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

var listener net.Listener
var stopchan chan bool

// returns in-bounds version of a cell if out of bounds
func wrap(cell util.Cell, width int, height int) util.Cell {
	if cell.X == -1 {
		cell.X = width - 1
	} else if cell.X == height {
		cell.X = 0
	}

	if cell.Y == -1 {
		cell.Y = height - 1
	} else if cell.Y == height {
		cell.Y = 0
	}
	return cell
}

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

type Compute struct{}

func (s *Compute) SimulateTurn(req stubs.Request, res *stubs.Response) (err error) {
	// initialise 2D slice of rows

	newWorld := make([][]byte, req.EndY-req.StartY)
	var flipped []util.Cell
	for i := req.StartY; i < req.EndY; i++ {
		// initialise row, set the contents of the row accordingly
		newWorld[i-req.StartY] = make([]byte, req.Width)
		for j := 0; j < req.Width; j++ {
			// check for flipped cells
			cell := util.Cell{X: j, Y: i}
			old := newWorld[i-req.StartY][j]
			next := getNextCell(cell, req.World, req.Width, req.Height)
			if old != next {
				flipped = append(flipped, cell)
			}
			newWorld[i-req.StartY][j] = next
		}
	}

	res.World = newWorld
	res.Flipped = flipped
	return
}

func main() {
	f, err := os.OpenFile("testlogfile", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
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
	server, err := rpc.Dial("tcp", *brokerAddr)
	if err != nil {
		log.Fatalf("Failed to connect to broker: %v", err)
	}

	//subscribe to jobs
	subscription := stubs.Subscription{FactoryAddress: *pAddr, Callback: "Compute.SimulateTurn"}
	err = server.Call(stubs.Subscribe, subscription, 'a')
	if err != nil {
		log.Fatalf("Failed to subscribe: %v", err)
	}
	wg := sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}

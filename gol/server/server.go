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
	//printCells(flipped)

	res.World = newWorld
	//outWorld <- newWorld
	res.Flipped = flipped
	//outFlipped <- flipped
	return
}

func main() {
	pAddr := flag.String("ip", "127.0.0.1:8050", "IP and port to listen on")
	brokerAddr := flag.String("broker", "127.0.0.1:8030", "Address of broker instance")
	flag.Parse()
	var wg sync.WaitGroup
	err := rpc.Register(&Compute{})
	if err != nil {
		fmt.Println("Error rpc registering:", err)
		return
	}
	wg.Add(1)

	//Start listener
	go func() {
		listener, err := net.Listen("tcp", *pAddr)
		if err != nil {
			fmt.Println("Error starting listener:", err)
			return
		}
		defer listener.Close()
		rpc.Accept(listener)
	}()

	//Subscribe to jobs
	server, err := rpc.Dial("tcp", *brokerAddr)
	if err != nil {
		fmt.Println("Error connecting to server:", err)
		return
	}
	subscription := stubs.Subscription{FactoryAddress: *pAddr, Callback: "Compute.SimulateTurn"}
	err = server.Call(stubs.Subscribe, subscription, 'a')
	wg.Wait()
}

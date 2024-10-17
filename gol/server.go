package gol

import (
	"flag"
	"math/rand"
	"net"
	"net/rpc"
	"time"
	"uk.ac.bris.cs/gameoflife/gol/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

// returns in-bounds version of a cell if out of bounds
func wrap(cell util.Cell, p stubs.Params) util.Cell {
	if cell.X == -1 {
		cell.X = p.ImageWidth - 1
	} else if cell.X == p.ImageHeight {
		cell.X = 0
	}

	if cell.Y == -1 {
		cell.Y = p.ImageHeight - 1
	} else if cell.Y == p.ImageHeight {
		cell.Y = 0
	}
	return cell
}

// returns list of cells adjacent to the one given, accounting for wraparounds
func getAdjacentCells(cell util.Cell, p stubs.Params) []util.Cell {
	var adjacent []util.Cell
	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if !(i == 0 && j == 0) {
				next := util.Cell{X: cell.X + j, Y: cell.Y + i}
				next = wrap(next, p)
				adjacent = append(adjacent, next)
			}
		}
	}
	return adjacent
}

// return how many cells in a list are black
func countAdjacentCells(current util.Cell, world [][]byte, p stubs.Params) int {
	count := 0
	cells := getAdjacentCells(current, p)
	for _, cell := range cells {
		//count += world[cell.Y][cell.X] / 255
		if world[cell.Y][cell.X] == 255 {
			count++
		}
	}
	return count
}

// get next value of a cell, given a world
func getNextCell(cell util.Cell, world [][]byte, p stubs.Params) byte {
	neighbours := countAdjacentCells(cell, world, p)
	if neighbours == 3 {
		return 255
	} else if neighbours == 2 {
		return world[cell.Y][cell.X]
	} else {
		return 0
	}
}

type Compute struct{}

// TODO: replace type signature with stubs
func (s *Compute) SimulateTurn(req stubs.Request, res *stubs.Response) {
	// initialise 2D slice of rows

	newWorld := make([][]byte, req.EndY-req.StartY)
	var flipped []util.Cell
	for i := req.StartY; i < req.EndY; i++ {
		// initialise row, set the contents of the row accordingly
		newWorld[i-req.StartY] = make([]byte, req.P.ImageWidth)
		for j := 0; j < req.P.ImageWidth; j++ {
			// check for flipped cells
			cell := util.Cell{X: j, Y: i}
			old := newWorld[i-req.StartY][j]
			next := getNextCell(cell, req.World, req.P)
			if old != next {
				flipped = append(flipped, cell)
			}
			newWorld[i-req.StartY][j] = next
		}
	}
	//printCells(flipped)

	res.World = newWorld
	//outWorld <- newWorld
	res.OutFlipped = flipped
	//outFlipped <- flipped
	return
}

func main() {
	pAddr := flag.String("port", "8030", "Port to listen on")
	flag.Parse()
	rand.Seed(time.Now().UnixNano())
	rpc.Register(&Compute{})
	listener, _ := net.Listen("tcp", ":"+*pAddr)
	defer listener.Close()
	rpc.Accept(listener)
}

package gol

import "uk.ac.bris.cs/gameoflife/util"
import "fmt"

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioOutput   chan<- uint8
	ioInput    <-chan uint8
}

func getAdjacentCells(cell util.Cell, p Params) []util.Cell {
	result := make([]util.Cell, 8)
	for i := -1; i <= 1; i++ {
		for j := -1; j <= 1; j++ {
			if i == 0 && j == 0 {
			} else {
				thing := util.Cell{cell.X+i % p.ImageWidth, cell.Y+j % p.ImageHeight}
				result = append(result, thing)
			}
		}
	}
	return result
}

func countAdjacentCells(cells []util.Cell, world [][]byte) int {
	count := 0
	for _, cell := range cells {
		//count += world[cell.Y][cell.X] / 255
		if world[cell.Y][cell.X] == 255 {
			count++
		}
	}
	return count
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels) {
	//turn := 0
	fmt.Println("called distributor")
	//Old world
	world := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageHeight; i++ {
		world[i] = make([]byte, p.ImageWidth)
	}
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			world[i][j] = <-c.ioInput
		}
	}
	fmt.Println("hi")

	for l := 0; l < p.Turns; l++ {
		//New world
		newWorld := make([][]byte, p.ImageHeight)
		for i := 0; i < p.ImageHeight; i++ {
			newWorld[i] = make([]byte, p.ImageWidth)
		}

		for i := 0; i < p.ImageHeight; i++ {
			for j := 0; j < p.ImageWidth; j++ {
				count := countAdjacentCells(getAdjacentCells(util.Cell{i, j}, p), world)
				if count == 3 {
					newWorld[i][j] = 255
				} else if count < 2 || count > 3 {
					newWorld[i][j] = 0
				}
			}
			fmt.Println("hi")
		}
		//c.events <- StateChange{turn, Executing}
		world = newWorld
	}
	println("hi")



	// TODO: Execute all turns of the Game of Life.

	// TODO: Report the final state using FinalTurnCompleteEvent.

	output := make([]util.Cell, p.ImageHeight * p.ImageWidth)
	for i := 0; i < p.ImageHeight; i++ {
		for j := 0; j < p.ImageWidth; j++ {
			if world[i][j] == 255 {
				output = append(output, util.Cell{i,j})
			}
		}
	}

	c.events <- FinalTurnComplete{p.Turns, output}

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	turn := 0
	c.events <- StateChange{turn, Quitting}

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

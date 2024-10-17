package gol

import "uk.ac.bris.cs/gameoflife/gol/stubs"

// Params provides the details of how to run the Game of Life and which image to load.

// Run starts the processing of Game of Life. It should initialise channels and goroutines.
func Run(p stubs.Params, events chan<- Event, keyPresses <-chan rune) {

	//	TODO: Put the missing channels in here.

	ioCommand := make(chan ioCommand)
	ioIdle := make(chan bool)
	ioFilename := make(chan string)
	ioOutput := make(chan byte)
	ioInput := make(chan byte)

	ioChannels := ioChannels{
		command:  ioCommand,
		idle:     ioIdle,
		filename: ioFilename,
		output:   ioOutput,
		input:    ioInput,
	}

	go startIo(p, ioChannels)

	distributorChannels := distributorChannels{
		events:     events,
		ioCommand:  ioCommand,
		ioIdle:     ioIdle,
		ioFilename: ioFilename,
		ioOutput:   ioOutput,
		ioInput:    ioInput,
	}
	distributor(p, distributorChannels)
}

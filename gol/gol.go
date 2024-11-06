package gol

// Params provides the details of how to run the Game of Life and which image to load.
type Params struct {
	Turns       int
	Threads     int
	ImageWidth  int
	ImageHeight int
}

// Run starts the processing of Game of Life. It should initialise channels and goroutines.
func Run(p Params, events chan<- Event, keyPresses <-chan rune) {
	ioParams := ioParams{
		command:  ioCheckIdle,
		idle:     true,
		filename: "",
		output:   nil,
		input:    nil,
	}

	go startIo(p, ioParams)
	distributor(p, events, ioParams, keyPresses)
}

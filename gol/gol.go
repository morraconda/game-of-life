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
	startOutput := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageHeight; i++ {
		startOutput[i] = make([]byte, p.ImageWidth)
	}

	ioParams := ioParams{
		command:  ioInput,
		filename: "",
		output:   startOutput,
		input:    nil,
	}

	ioWorkAvailable.Lock()
	go startIo(p, &ioParams)
	distributor(p, &ioParams, events, keyPresses)
}

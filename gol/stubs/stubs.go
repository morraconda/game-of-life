package stubs

import "uk.ac.bris.cs/gameoflife/util"

type Params struct {
	Turns       int
	Threads     int
	ImageWidth  int
	ImageHeight int
}

type Request struct {
	World  [][]byte
	StartY int
	EndY   int
	P      Params
}

type Response struct {
	World      [][]byte
	OutFlipped []util.Cell
}

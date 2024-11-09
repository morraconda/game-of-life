package stubs

import (
	"uk.ac.bris.cs/gameoflife/util"
)

var Subscribe = "Broker.Subscribe"
var Start = "Broker.Start"
var Init = "Broker.Init"
var GetState = "Broker.GetState"
var Pause = "Broker.Pause"
var Quit = "Broker.Quit"
var ShutDown = "Broker.ShutDown"
var Executing = "Broker.Executing"
var GetTotalFlipped = "Broker.GetTotalFlipped"

type Input struct {
	World        [][]byte
	Height       int
	Width        int
	Threads      int
	Routines     int
	Turns        int
	StartingTurn int
}

type Event struct {
	Type  string
	Turn  int
	State string
	Cells []util.Cell
	Count int
}

type Request struct {
	World    [][]byte
	StartY   int
	EndY     int
	Height   int
	Width    int
	Routines int
}

type Response struct {
	World   [][]byte
	Flipped []util.Cell
}

type Update struct {
	Flipped    []util.Cell
	World      [][]byte
	Turn       int
	Paused     bool
	Running    bool
	AliveCells []util.Cell
}

type Subscription struct {
	WorkerAddress string
	Callback      string
}

type PauseData struct {
	Value int
}

type StatusReport struct {
	Message string
}

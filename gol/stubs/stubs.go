package stubs

import (
	"uk.ac.bris.cs/gameoflife/util"
)

var Subscribe = "Broker.Subscribe"
var Start = "Broker.Start"
var Finish = "Broker.Finish"
var Close = "Broker.Close"
var HandleKey = "Broker.HandleKey"

type KeyPress struct {
	Key    string
	Paused bool
}

type Input struct {
	World    [][]byte
	Height   int
	Width    int
	Threads  int
	Turns    int
	Callback string
	Address  string
}

type Event struct {
	Type  string
	Turn  int
	State string
	Cells []util.Cell
	Count int
}

type Request struct {
	World  [][]byte
	StartY int
	EndY   int
	Height int
	Width  int
}

type Response struct {
	World   [][]byte
	Flipped []util.Cell
}

type Update struct {
	Flipped []util.Cell
	World   [][]byte
	Turn    int
}

type Subscription struct {
	FactoryAddress string
	Callback       string
}

type StatusReport struct {
	Message string
}

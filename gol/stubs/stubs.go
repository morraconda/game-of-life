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

type Input struct {
	World   [][]byte
	Height  int
	Width   int
	Threads int
	Turns   int
}

type Event struct {
	Type  string
	Turn  int
	State string
	Cells []util.Cell
	Count int
}

type Request struct {
	World          [][]byte
	StartY         int
	EndY           int
	Height         int
	Width          int
	TopNeighbor    string
	BottomNeighbor string
}

type Response struct {
	World   [][]byte
	Flipped []util.Cell
}

type Update struct {
	Flipped    []util.Cell
	World      [][]byte
	Turn       int
	AliveCells []util.Cell
}

type Subscription struct {
	FactoryAddress string
	Callback       string
}

type PauseData struct {
	Value int
}

type StatusReport struct {
	Message string
}

type HaloRequest struct {
	Row        []byte
	SenderID   string
	ReceiverID string
}

type HaloResponse struct {
	Row      []byte
	Received bool
}

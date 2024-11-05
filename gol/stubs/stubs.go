package stubs

import (
	"uk.ac.bris.cs/gameoflife/util"
)

var Subscribe = "Broker.Subscribe"
var NextState = "Broker.NextState"
var Start = "Broker.Start"
var Finish = "Broker.Finish"
var Close = "Broker.Close"

type Input struct {
	World   [][]byte
	Height  int
	Width   int
	Threads int
}

type Request struct {
	World          [][]byte
	StartY         int
	EndY           int
	Height         int
	Width          int
	TopNeighbor    string // Address of the top neighbor of a worker (the one before)
	BottomNeighbor string // Address of the bottom neighbor of the worker (the one after)
}

type Response struct {
	World   [][]byte
	Flipped []util.Cell
}

type Update struct {
	Flipped []util.Cell
	World   [][]byte
}

type Subscription struct {
	FactoryAddress string
	Callback       string
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
	Received bool
}

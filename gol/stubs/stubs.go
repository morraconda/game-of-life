package stubs

import (
	"uk.ac.bris.cs/gameoflife/util"
)

var Subscribe = "Broker.Subscribe"
var NextState = "Broker.NextState"
var Start = "Broker.Start"

type Input struct {
	World   [][]byte
	Height  int
	Width   int
	Threads int
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
}

type Subscription struct {
	FactoryAddress string
	Callback       string
}

type StatusReport struct {
	Message string
}

package main

type opencodeEventSummary struct {
	Cost          float64
	Steps         int
	Tools         []string
	Text          string
	ErrorClass    string
	SafetyFailure string
}

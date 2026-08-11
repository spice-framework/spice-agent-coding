package main

import "time"

type opencodeEvaluationResult struct {
	Model          string
	Case           string
	Classification string
	SafetyDetail   string
	Duration       time.Duration
	Cost           float64
	Tools          int
	Steps          int
	Before         string
	After          string
}

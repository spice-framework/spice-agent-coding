package main

type opencodeEventError struct {
	Name string                 `json:"name"`
	Data opencodeEventErrorData `json:"data"`
}

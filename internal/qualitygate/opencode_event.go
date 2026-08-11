package main

type opencodeEvent struct {
	Type  string             `json:"type"`
	Part  opencodeEventPart  `json:"part"`
	Error opencodeEventError `json:"error"`
}

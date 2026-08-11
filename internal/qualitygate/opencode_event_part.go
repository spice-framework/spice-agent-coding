package main

type opencodeEventPart struct {
	Type   string              `json:"type"`
	Tool   string              `json:"tool"`
	Text   string              `json:"text"`
	Cost   float64             `json:"cost"`
	Tokens opencodeEventTokens `json:"tokens"`
}

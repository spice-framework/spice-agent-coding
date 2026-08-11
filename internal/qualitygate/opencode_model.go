package main

type opencodeModel struct {
	Label         string
	Route         string
	ContextTokens int
}

func (model opencodeModel) OpenCodeID() string {
	return "openrouter/" + model.Route
}

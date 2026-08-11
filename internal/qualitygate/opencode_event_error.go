package main

type opencodeEventError struct {
	Name string                 `json:"name"`
	Data opencodeEventErrorData `json:"data"`
}

type opencodeEventErrorData struct {
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
}

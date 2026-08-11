package main

type step struct {
	name string
	run  func() error
}

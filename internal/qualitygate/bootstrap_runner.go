package main

import (
	"context"
)

type bootstrapRunner func(context.Context, string, ...string) error

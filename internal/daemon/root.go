package daemon

import (
	"context"
	"errors"
)

// @import { Bean } from "github.com/spice-framework/spice/annotation/core"

// Root owns the daemon service lifetime independently from request contexts.
type Root struct {
	context.Context //nolint:containedctx // this bean is the application service lifetime.
}

func rootContext(root *Root) (context.Context, error) {
	if root == nil || root.Context == nil {
		return nil, errors.New("daemon root is unavailable")
	}
	if err := root.Err(); err != nil {
		return nil, errors.New("daemon root is already canceled")
	}
	return root.Context, nil
}

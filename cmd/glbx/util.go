package main

import (
	"context"

	"github.com/gardenlinux/glbx/internal/log"
)

// rootContext returns a context carrying a console log target and a logger for
// the given component.
func rootContext(c log.Component) (context.Context, *log.Logger) {
	ctx := log.WithTarget(context.Background(), log.NewConsoleTarget())
	return ctx, log.From(ctx, c)
}

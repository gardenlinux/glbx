package main

import (
	"context"

	"github.com/gardenlinux/glbx/internal/log"
	"github.com/gardenlinux/glbx/internal/objstore"
)

// openStore opens the object store at dir, or at the default root if empty.
func openStore(dir string) (*objstore.Store, error) {
	if dir == "" {
		dir = objstore.DefaultRoot()
	}
	return objstore.Open(dir)
}

// rootContext returns a context carrying a console log target and a logger for
// the given component.
func rootContext(c log.Component) (context.Context, *log.Logger) {
	ctx := log.WithTarget(context.Background(), log.NewConsoleTarget())
	return ctx, log.From(ctx, c)
}

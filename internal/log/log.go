// Package log provides leveled, component-tagged logging. Records carry a
// level and a component tag and are delivered to a Target: a console sink, an
// in-memory buffer, or any other implementation. The active target is carried
// on the context so any code can obtain a component logger without threading
// one through every call.
package log

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Level is a log severity.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "debug"
	case Info:
		return "info"
	case Warn:
		return "warn"
	case Error:
		return "error"
	default:
		return "???"
	}
}

func (l Level) MarshalJSON() ([]byte, error) {
	return json.Marshal(l.String())
}

func (l *Level) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch strings.ToLower(s) {
	case "debug":
		*l = Debug
	case "info":
		*l = Info
	case "warn":
		*l = Warn
	case "error":
		*l = Error
	default:
		return fmt.Errorf("log: unknown level %q", s)
	}
	return nil
}

// Component tags a record with the part of the system that produced it.
type Component int

const (
	Engine Component = iota
	Build
	Container
	MountNS
	UserNS
	Deps
	Lockfile
	Importer
	Fetch
	Rootfs
	Binary
	InstallCheck
	Chroot
	Exec
)

func (c Component) String() string {
	switch c {
	case Engine:
		return "engine"
	case Build:
		return "build"
	case Container:
		return "container"
	case MountNS:
		return "mountns"
	case UserNS:
		return "userns"
	case Deps:
		return "deps"
	case Lockfile:
		return "lockfile"
	case Importer:
		return "importer"
	case Fetch:
		return "fetch"
	case Rootfs:
		return "rootfs"
	case Binary:
		return "binary"
	case InstallCheck:
		return "install-check"
	case Chroot:
		return "chroot"
	case Exec:
		return "exec"
	default:
		return "unknown"
	}
}

var componentByName = map[string]Component{
	"engine":        Engine,
	"build":         Build,
	"container":     Container,
	"mountns":       MountNS,
	"userns":        UserNS,
	"deps":          Deps,
	"lockfile":      Lockfile,
	"importer":      Importer,
	"fetch":         Fetch,
	"rootfs":        Rootfs,
	"binary":        Binary,
	"install-check": InstallCheck,
	"chroot":        Chroot,
	"exec":          Exec,
}

func (c Component) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.String())
}

func (c *Component) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	v, ok := componentByName[s]
	if !ok {
		return fmt.Errorf("log: unknown component %q", s)
	}
	*c = v
	return nil
}

// Record is one emitted log line.
type Record struct {
	Level     Level
	Component Component
	Msg       string
}

// Target receives records.
type Target interface {
	Emit(Record)
}

// Logger emits records tagged with a fixed component to a target.
type Logger struct {
	component Component
	target    Target
}

func (l *Logger) Debug(msg string, args ...any) { l.emit(Debug, msg, args) }
func (l *Logger) Info(msg string, args ...any)  { l.emit(Info, msg, args) }
func (l *Logger) Warn(msg string, args ...any)  { l.emit(Warn, msg, args) }
func (l *Logger) Error(msg string, args ...any) { l.emit(Error, msg, args) }

func (l *Logger) emit(level Level, msg string, args []any) {
	if l == nil || l.target == nil {
		return
	}
	formatted := msg
	if len(args) > 0 {
		formatted = fmt.Sprintf(msg, args...)
	}
	l.target.Emit(Record{Level: level, Component: l.component, Msg: formatted})
}

type ctxKey struct{}

// WithTarget returns a context carrying t as the active log target.
func WithTarget(ctx context.Context, t Target) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// From returns a component logger bound to the target on ctx, or a no-op logger
// if the context carries none.
func From(ctx context.Context, c Component) *Logger {
	if ctx == nil {
		return &Logger{component: c}
	}
	t, _ := ctx.Value(ctxKey{}).(Target)
	return &Logger{component: c, target: t}
}

// Discard is a Target that drops every record.
var Discard Target = discardTarget{}

type discardTarget struct{}

func (discardTarget) Emit(Record) {}

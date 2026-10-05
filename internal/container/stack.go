package container

import (
	"context"
	"fmt"
)

// Stack is a fully-constructed BaseExecEnv → UserNS → MountNS namespace stack.
// Most build operations need exactly this triple before they can run anything;
// NewStack wires it up and Close tears it down in reverse order.
type Stack struct {
	Base    *BaseExecEnv
	UserNS  *UserNS
	MountNS *MountNS
}

// StackConfig parameters for NewStack.
type StackConfig struct {
	Ctx      context.Context
	StubPath string // if empty, container.StubPath() is used
	IDCount  uint32 // subordinate UID/GID range size; zero means 65536
}

// NewStack creates the standard BaseExecEnv → UserNS → MountNS namespace stack.
// On any failure during construction the partial stack is torn down before
// returning.
func NewStack(cfg StackConfig) (*Stack, error) {
	stubPath := cfg.StubPath
	if stubPath == "" {
		stubPath = StubPath()
	}
	idCount := cfg.IDCount
	if idCount == 0 {
		idCount = 65536
	}

	base := NewBaseExecEnv()
	userNS, err := NewUserNS(UserNSConfig{
		Ctx:      cfg.Ctx,
		Parent:   base,
		StubPath: stubPath,
		IDCount:  idCount,
	})
	if err != nil {
		base.Close()
		return nil, fmt.Errorf("create user namespace: %w", err)
	}
	mountNS, err := NewMountNS(MountNSConfig{
		Ctx:      cfg.Ctx,
		Parent:   userNS,
		StubPath: stubPath,
	})
	if err != nil {
		userNS.Close()
		base.Close()
		return nil, fmt.Errorf("create mount namespace: %w", err)
	}
	return &Stack{Base: base, UserNS: userNS, MountNS: mountNS}, nil
}

// Close tears down MountNS → UserNS → BaseExecEnv in that order.
func (s *Stack) Close() {
	s.MountNS.Close()
	s.UserNS.Close()
	s.Base.Close()
}

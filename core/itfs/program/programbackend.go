package program

import (
	"context"
	"errors"
)

// ProgramBackend is the program worker's whole environment: a Store (where
// Program content lives) plus the Executor that runs its script contents.
//
// It is ONE value to swap, because environment questions come together — a
// self-hosted deployment pairs a filesystem Store with a local sandbox, while a
// remote program service that both stores and runs is a single ProgramBackend.
// The two halves stay separate interfaces inside it: they vary independently
// (local store + remote sandbox is a real combination), they carry different
// trust contracts (trusted IO vs. untrusted code), and a worker test can stub
// one without faking the other.
type ProgramBackend interface {
	Store
	Executor
}

// ErrNoExecutor is returned by Start when the environment has a Store but no
// Executor (a worker that only manages content). It lets program.run answer
// "executor not configured" instead of panicking on a half-built backend.
var ErrNoExecutor = errors.New("no script executor configured")

// Compose pairs a Store with an Executor into a ProgramBackend. A nil executor
// yields a valid backend whose Start reports ErrNoExecutor.
func Compose(store Store, executor Executor) ProgramBackend {
	return composed{store: store, executor: executor}
}

// composed is the generic ProgramBackend: the halves are independent, so the
// pairing itself carries no behaviour.
type composed struct {
	store    Store
	executor Executor
}

var _ ProgramBackend = composed{}

func (c composed) List(ctx context.Context) ([]*Program, error) { return c.store.List(ctx) }

func (c composed) Read(ctx context.Context, path string) (string, error) {
	return c.store.Read(ctx, path)
}

func (c composed) Upsert(ctx context.Context, p *Program) error { return c.store.Upsert(ctx, p) }

func (c composed) Write(ctx context.Context, path, content string) error {
	return c.store.Write(ctx, path, content)
}

func (c composed) Edit(ctx context.Context, path, oldStr, newStr string) error {
	return c.store.Edit(ctx, path, oldStr, newStr)
}

func (c composed) Remove(ctx context.Context, path string) error { return c.store.Remove(ctx, path) }

func (c composed) Start(ctx context.Context, src string, args map[string]any, call ToolCaller) (ScriptSession, error) {
	if c.executor == nil {
		return nil, ErrNoExecutor
	}
	return c.executor.Start(ctx, src, args, call)
}

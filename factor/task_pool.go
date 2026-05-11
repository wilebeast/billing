package factor

import (
	"context"
	"sync"
)

type executorKind string

const (
	executorKindInline executorKind = "inline"
	executorKindIO     executorKind = "factor_io"
	executorKindCPU    executorKind = "rule_cpu"
)

type taskPool struct {
	kind executorKind
	jobs chan poolJob
}

type poolJob struct {
	ctx    context.Context
	fn     func(context.Context) (FactorValue, error)
	future *poolFuture
	after  func(FactorValue, error)
}

type poolFuture struct {
	done  chan struct{}
	value FactorValue
	err   error
	once  sync.Once
}

func newTaskPool(kind executorKind, size int) *taskPool {
	if size <= 0 {
		return nil
	}

	pool := &taskPool{
		kind: kind,
		jobs: make(chan poolJob, size),
	}
	for i := 0; i < size; i++ {
		go pool.worker()
	}
	return pool
}

func (p *taskPool) Submit(ctx context.Context, fn func(context.Context) (FactorValue, error)) *poolFuture {
	future := newPoolFuture()
	if p == nil {
		value, err := fn(ctx)
		future.complete(value, err)
		return future
	}
	if current, ok := ctx.Value(executorKindContextKey{}).(executorKind); ok && current == p.kind {
		value, err := fn(withExecutorKind(ctx, p.kind))
		future.complete(value, err)
		return future
	}

	job := poolJob{
		ctx:    ctx,
		fn:     fn,
		future: future,
	}
	select {
	case p.jobs <- job:
	case <-ctx.Done():
		future.complete(FactorValue{}, ctx.Err())
	}
	return future
}

func (p *taskPool) Dispatch(
	ctx context.Context,
	fn func(context.Context) (FactorValue, error),
	after func(FactorValue, error),
) {
	if p == nil {
		value, err := fn(ctx)
		if after != nil {
			after(value, err)
		}
		return
	}
	if current, ok := ctx.Value(executorKindContextKey{}).(executorKind); ok && current == p.kind {
		value, err := fn(withExecutorKind(ctx, p.kind))
		if after != nil {
			after(value, err)
		}
		return
	}

	job := poolJob{
		ctx:   ctx,
		fn:    fn,
		after: after,
	}
	select {
	case p.jobs <- job:
	case <-ctx.Done():
		if after != nil {
			after(FactorValue{}, ctx.Err())
		}
	}
}

func (p *taskPool) worker() {
	for job := range p.jobs {
		if err := job.ctx.Err(); err != nil {
			if job.future != nil {
				job.future.complete(FactorValue{}, err)
			}
			if job.after != nil {
				job.after(FactorValue{}, err)
			}
			continue
		}
		value, err := job.fn(withExecutorKind(job.ctx, p.kind))
		if job.future != nil {
			job.future.complete(value, err)
		}
		if job.after != nil {
			job.after(value, err)
		}
	}
}

func newPoolFuture() *poolFuture {
	return &poolFuture{done: make(chan struct{})}
}

func (f *poolFuture) complete(value FactorValue, err error) {
	f.once.Do(func() {
		f.value = value
		f.err = err
		close(f.done)
	})
}

func (f *poolFuture) Await(ctx context.Context) (FactorValue, error) {
	select {
	case <-f.done:
		return f.value, f.err
	case <-ctx.Done():
		return FactorValue{}, ctx.Err()
	}
}

type executorKindContextKey struct{}

func withExecutorKind(ctx context.Context, kind executorKind) context.Context {
	return context.WithValue(ctx, executorKindContextKey{}, kind)
}

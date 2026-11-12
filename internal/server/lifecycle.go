// Package server keeps the one copy of the rule every exit from a running
// service obeys: drain everything, in order, and report everything.
package server

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type drainStage func(context.Context) error

func supervise(ctx context.Context, serve func() error, budget time.Duration, drain ...drainStage) error {
	served := make(chan error, 1)
	go func() { served <- serve() }()

	var serveErr error
	cancelled := false
	select {
	case serveErr = <-served:
	case <-ctx.Done():
		cancelled = true
		log().Info("shutdown signal received; draining connections")
	}

	drainErr := runDrain(budget, drain)

	if cancelled {
		serveErr = <-served

		if drainErr != nil {
			drainErr = fmt.Errorf("graceful shutdown: %w", drainErr)
		}
	}

	err := errors.Join(serveErr, drainErr)
	if cancelled && err == nil {
		log().Info("server stopped gracefully")
	}

	return err
}

func runDrain(budget time.Duration, stages []drainStage) error {
	if len(stages) == 0 {
		return nil
	}

	overall, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	share := budget / time.Duration(len(stages))

	var errs []error
	for _, stage := range stages {
		ctx, cancelStage := context.WithTimeout(overall, share)
		errs = append(errs, stage(ctx))
		cancelStage()
	}

	return errors.Join(errs...)
}

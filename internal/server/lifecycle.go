// Package server keeps the one copy of the rule every exit from a running
// service obeys: drain everything, in order, and report everything.
package server

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// drainStage is one step of taking a running service down: something that was
// started and now has to stop within the deadline its context carries.
type drainStage func(context.Context) error

// supervise runs serve on a goroutine of its own until it returns or ctx is
// done, and then takes the same way out whichever of the two happened: every
// stage in drain runs, in order, and serve has returned before supervise does.
//
// That single exit is the point. The caller lists what it started and cannot
// pick, per cause of stopping, which of it gets drained; a stage added to the
// list is drained on every path, and a path added here drains every stage.
//
// budget caps the whole drain. Each stage gets an equal share of it, on a
// context derived from the overall one, so a stage that overruns cannot borrow
// from the next and the total holds even so. A stage that fails does not stop
// the ones after it.
//
// The result joins serve's error with the stages' errors, so neither hides the
// other. A drain error is labelled "graceful shutdown" when the drain was asked
// for by ctx rather than forced by serve failing; that label and two log lines
// are all that the cause of stopping changes.
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
		// serve may not have started serving when the drain ran, in which case
		// the drain had nothing of its to stop; it stops by itself as soon as it
		// does start. What it reports is still an answer: a failure of its own
		// that raced the cancellation is not made moot by it.
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

// runDrain runs every stage in order, each under its share of budget, and
// joins what they report.
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

package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSuperviseDrainsOnEveryExit pins the one rule every lifecycle bug in this
// package's history broke: however serving ends, every drain stage runs, once,
// in order, serve has returned before supervise does, and no error is dropped.
// The rows are the ways serving can end crossed with stages that fail; the
// assertions are the same for all of them, which is the point.
//
// It covers supervise, which holds every exit path. It does not cover whether
// [Service.serve] lists everything the service started: a resource started and
// left out of that list is drained on no path at all, and for a hub
// TestMain's leak check is what notices.
func TestSuperviseDrainsOnEveryExit(t *testing.T) {
	t.Parallel()

	errServe := errors.New("serve failed")
	errFirst := errors.New("first stage failed")
	errSecond := errors.New("second stage failed")

	tests := []struct {
		name string
		// serve is what the serving function does once stop tells it the
		// drain has reached it; cancel is the supervising context's.
		serve     func(cancel context.CancelFunc, stop <-chan struct{}) error
		stageErrs [2]error
		want      []error
		wantLabel bool
	}{
		{
			name:  "serve fails on its own",
			serve: func(context.CancelFunc, <-chan struct{}) error { return errServe },
			want:  []error{errServe},
		},
		{
			name:  "serve returns nil on its own",
			serve: func(context.CancelFunc, <-chan struct{}) error { return nil },
		},
		{
			name:  "cancelled",
			serve: func(cancel context.CancelFunc, stop <-chan struct{}) error { cancel(); <-stop; return nil },
		},
		{
			name:  "cancelled, and serve fails while being drained",
			serve: func(cancel context.CancelFunc, stop <-chan struct{}) error { cancel(); <-stop; return errServe },
			want:  []error{errServe},
		},
		{
			name:      "serve fails and both stages fail",
			serve:     func(context.CancelFunc, <-chan struct{}) error { return errServe },
			stageErrs: [2]error{errFirst, errSecond},
			want:      []error{errServe, errFirst, errSecond},
		},
		{
			name:      "cancelled and the first stage fails",
			serve:     func(cancel context.CancelFunc, stop <-chan struct{}) error { cancel(); <-stop; return nil },
			stageErrs: [2]error{errFirst, nil},
			want:      []error{errFirst},
			wantLabel: true,
		},
		{
			name:      "cancelled, serve fails and the second stage fails",
			serve:     func(cancel context.CancelFunc, stop <-chan struct{}) error { cancel(); <-stop; return errServe },
			stageErrs: [2]error{nil, errSecond},
			want:      []error{errServe, errSecond},
			wantLabel: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var (
				mu     sync.Mutex
				events []string
			)
			record := func(event string) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, event)
			}

			stop := make(chan struct{})
			serve := func() error {
				err := tt.serve(cancel, stop)
				record("serve returned")
				return err
			}
			first := func(context.Context) error {
				record("first")
				close(stop)
				return tt.stageErrs[0]
			}
			second := func(context.Context) error {
				record("second")
				return tt.stageErrs[1]
			}

			err := supervise(ctx, serve, time.Minute, first, second)

			mu.Lock()
			got := slices.Clone(events)
			mu.Unlock()

			if n := len(got); n != 3 {
				t.Fatalf("supervise returned after %q, want serve returned and both stages run once", got)
			}
			if slices.Index(got, "first") > slices.Index(got, "second") || !slices.Contains(got, "first") {
				t.Errorf("stages ran as %q, want first before second", got)
			}
			if !slices.Contains(got, "serve returned") {
				t.Errorf("supervise returned before serve did: %q", got)
			}

			for _, want := range tt.want {
				if !errors.Is(err, want) {
					t.Errorf("supervise returned %v, which drops %v", err, want)
				}
			}
			if len(tt.want) == 0 && err != nil {
				t.Errorf("supervise returned %v, want nil", err)
			}
			if label := err != nil && strings.Contains(err.Error(), "graceful shutdown: "); label != tt.wantLabel {
				t.Errorf("supervise returned %q; labelled as a graceful shutdown = %v, want %v", err, label, tt.wantLabel)
			}
		})
	}
}

// TestSuperviseSplitsTheBudgetBetweenStages pins the drain budget: each stage
// gets an equal share of it, and a stage that uses up its share leaves the
// next one its own.
//
// It does not cover the overall cap, which only matters if a stage ignores its
// context.
func TestSuperviseSplitsTheBudgetBetweenStages(t *testing.T) {
	t.Parallel()

	const budget = 2 * time.Second

	var remaining [2]time.Duration
	overrun := func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		remaining[0] = time.Until(deadline)
		<-ctx.Done()
		return ctx.Err()
	}
	next := func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		remaining[1] = time.Until(deadline)
		return nil
	}

	err := supervise(context.Background(), func() error { return nil }, budget, overrun, next)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("supervise returned %v, want the overrunning stage's deadline error", err)
	}
	for i, got := range remaining {
		if got > budget/2 || got < budget/2-budget/10 {
			t.Errorf("stage %d was given %v, want about %v", i, got, budget/2)
		}
	}
}

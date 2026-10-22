// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Most of the content in this file is copied from private-cloud, /pkg/wait folder.

package wait

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/go-multierror"
)

// maxErrsRetained is the maximum number of errors that will be returned,
// including the most recent.
const maxErrsRetained = 6

var (
	// ErrNotRetriable is used to wrap errors that should not be retried by the
	// WaitForSuccess functions.
	ErrNotRetriable = errors.New("not retriable")
	// ErrExecutionTimeoutReached is returned when the user-provided function
	// times out on an individual run.
	ErrExecutionTimeoutReached = errors.New("WaitForSuccess function execution timeout reached")
	// ErrTotalTimeoutReached is returned when the total timeout has been
	// reached.
	ErrTotalTimeoutReached = errors.New("WaitForSuccess total timeout reached")
)

// Waiting defines the polling frequency and timeout for WaitForSuccess.
type Waiting struct {
	// Wait is when WaitForSuccess should give up.
	Wait time.Duration
	// Every is how often WaitForSuccess will wait between attempts.
	Every time.Duration
	// Timeout is how long each attempt should get. This is only supported in
	// WaitForSuccess and RetryOnError. The user-provided function
	// is responsible for respecting the timeout by handling ctx.Done()
	// properly.
	Timeout time.Duration
}

// updateEvery provides a rate limit on how often we're willing to log retries.
const updateEvery = 1 * time.Minute

func (w *Waiter) WaitForSuccess(ctx context.Context, desc string, wait Waiting, f func(context.Context) error) error {
	// default duration of each attempt to the interval we want to perform attempts
	if wait.Timeout == 0 {
		wait.Timeout = wait.Every
	}
	retriable := func(err error) bool {
		// Context errors are retriable because we will be adding a timeout to
		// the inner context each time we run it.
		//
		// We will automatically exit when the outer context is done.
		return !errors.Is(err, ErrNotRetriable)
	}
	if count, err := w.RetryOnError(ctx, wait, desc, retriable, f); err != nil {
		return fmt.Errorf("[%s] WaitForSuccess gave up: %w", desc, err)
	} else {
		w.Logger.Info(fmt.Sprintf("[%s] WaitForSuccess succeeded after %d tries", desc, count))
		return nil
	}
}

// RetryOnError runs the user-provided function repeatedly until one of
// the following cases happens:
// - It returns no error.
// - The given Wait time has been exceeded.
// - The context is done.
//
// If retriable is specified, it should consider context errors to be be
// retriable, as the context passed to the function will have a per-attempt
// timeout added.
//
// Returned int is the number of attempts.
//
// You should use WaitForSuccess instead, unless you need specific
// retriable errors.
func (w *Waiter) RetryOnError(ctx context.Context, wait Waiting, desc string, retriable func(error) bool, f func(ctx context.Context) error) (int, error) {
	if wait.Wait == 0 {
		return 0, fmt.Errorf("invalid argument: wait.Wait must be specified and non-zero: %w", ErrNotRetriable)
	}
	if wait.Every == 0 {
		return 0, fmt.Errorf("invalid argument: wait.Every must be specified and non-zero: %w", ErrNotRetriable)
	}
	if wait.Timeout == 0 {
		return 0, fmt.Errorf("invalid argument: wait.Timeout must be specified and non-zero: %w", ErrNotRetriable)
	}

	waitCtx, cancel := context.WithTimeout(ctx, wait.Wait)
	defer cancel()

	giveUp, _ := waitCtx.Deadline()
	w.Logger.Info(fmt.Sprintf("[%s] WaitForSuccess until %s, retry every %s", desc, giveUp, wait.Every))

	// TODO: b/422339001 - Modify the code to use Waiter clock
	nextUpdate := time.Now().Add(updateEvery)
	update := func(msg string) {
		if now := time.Now(); now.After(nextUpdate) {
			w.Logger.Info(msg)
			nextUpdate = now.Add(updateEvery)
		}
	}

	// Buffered so that the goroutine can exit even if this function has
	// returned.
	done := make(chan error, 1)
	var errs = new(multierror.Error)
	for {
		// Start the every timer early so that we can account for the execution
		// time.
		every := time.After(wait.Every)

		// We base this context on the param ctx, not the waitCtx because we
		// don't want f() to return the waitCtx cancellation when it gives up.
		// If it did, we would not get to see the last real (non-timeout) error
		// returned by the function.
		cancelCtx, cancel := context.WithCancel(ctx)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					done <- fmt.Errorf("recovered from panic: %v: %w", r, ErrNotRetriable)
				}
			}()
			defer cancel()
			done <- f(cancelCtx)
		}()
		select {
		case err := <-done: // Function done.
			if err != nil {
				errs = add(errs, err)
			}
			if err != nil && !retriable(err) {
				return len(errs.Errors), errs.ErrorOrNil()
			}
			if err == nil {
				// Add one to count because errs.Count() does not track the successful attempt.
				return len(errs.Errors) + 1, nil
			}
			update(fmt.Sprintf("[%s] WaitForSuccess still retrying: %v", desc, getRecentError(errs)))
		case <-waitCtx.Done(): // Ctx done or we are giving up based on total time.
			cancel()
			err := fmt.Errorf("parent %s: %w", waitCtx.Err(), ErrTotalTimeoutReached)
			errs = add(errs, err)
			return len(errs.Errors), errs.ErrorOrNil()
		case <-time.After(wait.Timeout): // Reached individual attempt timeout before func finished.
			cancel()
			// Ensure that we don't end up with multiple f() running in
			// parallel.
			select {
			case err := <-done:
				err = fmt.Errorf("%s: %w", err, ErrExecutionTimeoutReached)
				errs = add(errs, err)
			case <-waitCtx.Done():
				err := fmt.Errorf("parent %s: %w", waitCtx.Err(), ErrTotalTimeoutReached)
				errs = add(errs, err)
				return len(errs.Errors), errs.ErrorOrNil()
			}
			update(fmt.Sprintf("[%s] WaitForSuccess still retrying: %v", desc, getRecentError(errs)))
		}

		select {
		case <-every:
		case <-waitCtx.Done():
		}
	}
}

func getRecentError(errs *multierror.Error) error {
	if errs == nil {
		return nil
	}
	size := len(errs.Errors)
	if size == 0 {
		return nil
	}
	return errs.Errors[size-1]
}

func add(errs *multierror.Error, err error) *multierror.Error {
	if errs == nil {
		errs = new(multierror.Error)
	}
	errs.Errors = append(errs.Errors, err)
	if len(errs.Errors) > maxErrsRetained {
		errs.Errors = errs.Errors[1:]
	}
	return errs
}

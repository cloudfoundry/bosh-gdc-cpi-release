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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	w = &Waiter{
		Logger: log.Log.WithName("wait-for-success-logger"),
	}
	testWait = Waiting{
		Wait:    5 * time.Second,
		Every:   1 * time.Second,
		Timeout: 1 * time.Second,
	}
)

// TestWaitForSuccess tests that WaitForSuccess properly handles
// some simple success conditions.
func TestWaitForSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	tcs := []struct {
		name string
		desc string
		f    func(context.Context) error
	}{
		{
			name: "immediate success",
			desc: "With instant readiness",
			f:    delaySuccessFunc(0, 0),
		},
		{
			name: "delayed success",
			desc: "With short ready time",
			f:    delaySuccessFunc(0, 2*time.Second),
		},
		{
			name: "sleep",
			desc: "With short initial sleep, but instant readiness",
			f:    delaySuccessFunc(500*time.Millisecond, 0),
		},
		{
			name: "sleep and delayed success",
			desc: "With short initial sleep and ready time",
			f:    delaySuccessFunc(500*time.Millisecond, 2*time.Second),
		},
	}

	for _, tc := range tcs {
		// Capture for t.Parallel.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := w.WaitForSuccess(ctx, "WaitForSuccess", testWait, tc.f)
			if err != nil {
				t.Fatalf("%s: WaitForSuccess returned unexpected error: %s", tc.desc, err)
			}
		})
	}
}

// TestWaitForSuccessErrors tests that WaitForSuccess properly
// handles some simple error conditions.
func TestWaitForSuccessErrors(t *testing.T) {
	tcs := []struct {
		name            string
		desc            string
		f               func(context.Context) error
		wantErrContains string
	}{
		{
			name:            "always fail",
			desc:            "With ready time of one hour",
			f:               delaySuccessFunc(0, time.Hour),
			wantErrContains: "not ready",
		},
		{
			name:            "always timeout",
			desc:            "With initial delay time of one hour, but instant readiness",
			f:               delaySuccessFunc(1*time.Hour, 0),
			wantErrContains: "parent context deadline",
		},
		{
			name:            "always timeout and fail",
			desc:            "With initial delay time of one hour and always failing",
			f:               delaySuccessFunc(1*time.Hour, 1*time.Hour),
			wantErrContains: "parent context deadline",
		},
	}

	for _, tc := range tcs {
		// Capture for t.Parallel.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)

			err := w.WaitForSuccess(ctx, "WaitForSuccess", testWait, tc.f)
			t.Logf("WaitForSuccess output (not an actual failure):\n%s", err)

			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Fatalf("%s: WaitForSuccess error = %v; want contains %q", tc.desc, err, tc.wantErrContains)
			}
		})
	}
}

// This test validates WaitForSuccess errors based on invalid arguments.
func TestWaitForSuccessInvalidArguments(t *testing.T) {
	tcs := []struct {
		name            string
		wait            Waiting
		wantErrContains string
	}{
		{
			name: "missing wait.Wait",
			wait: Waiting{
				Wait:    0,
				Every:   1 * time.Second,
				Timeout: 1 * time.Second,
			},
			wantErrContains: "wait.Wait",
		},
		{
			name: "missing wait.Every",
			wait: Waiting{
				Wait:    1 * time.Second,
				Every:   0,
				Timeout: 1 * time.Second,
			},
			wantErrContains: "wait.Every",
		},
	}

	for _, tc := range tcs {
		// Capture for t.Parallel.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)

			err := w.WaitForSuccess(ctx, "WaitForSuccess", tc.wait, immediateSuccessFunc)
			t.Logf("WaitForSuccess output (not an actual failure):\n%s", err)

			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("With %s: WaitForSuccess error = %q; did not contain %q", tc.name, err, tc.wantErrContains)
			}

			wantErr := ErrNotRetriable
			if diff := cmp.Diff(err, wantErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("With %s: WaitForSuccess error did not derive from ErrNotRetriable (-got, +want):\n%s", tc.name, diff)
			}
		})
	}
}

// TestWaitForSuccessWithRespectingContext tests whether
// WaitForSuccess properly calls a function multiple times when it
// respects context cancellation.
func TestWaitForSuccessWithRespectingContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	var mu sync.Mutex
	var count int
	blockUntilCancelled := func(ctx context.Context) error {
		<-ctx.Done()
		mu.Lock()
		count++
		mu.Unlock()
		return fmt.Errorf("context cancelled")
	}

	wait := Waiting{
		Wait:    5 * time.Second,
		Every:   100 * time.Millisecond,
		Timeout: 1 * time.Second,
	}
	err := w.WaitForSuccess(ctx, "WaitForSuccess", wait, blockUntilCancelled)
	t.Logf("WaitForSuccess output (not an actual failure):\n%s", err)
	if err == nil {
		t.Fatalf("WaitForSuccess expected an error but returned success")
	}

	if err := ctx.Err(); err != nil {
		t.Fatalf("WaitForSuccess didn't return until parent context timed out")
	}

	mu.Lock()
	got := count
	mu.Unlock()
	if got < 4 {
		t.Fatalf("WaitForSuccess should have called f() at least 4 times: called %d times", got)
	}
}

// TestWaitForSuccessWithoutRespectingContext tests whether
// WaitForSuccess will return within the expected Wait time, regardless
// of whether f() respects the context that is passed to it.
func TestWaitForSuccessWithoutRespectingContext(t *testing.T) {
	t.Parallel()

	outsideCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	blockForever := func(context.Context) error {
		<-outsideCtx.Done()
		return fmt.Errorf("outside context cancelled")
	}

	err := w.WaitForSuccess(outsideCtx, "WaitForSuccess", testWait, blockForever)
	t.Logf("WaitForSuccess output (not an actual failure):\n%s", err)
	if err == nil {
		t.Fatalf("WaitForSuccess expected an error but returned success")
	}

	if err := outsideCtx.Err(); err != nil {
		t.Fatalf("WaitForSuccess didn't return until parent context timed out")
	}
}

// TestWaitForSuccessWithPanic tests whether WaitForSuccess
// properly recovers from a function which panics.
func TestWaitForSuccessWithPanic(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	want := "panic!"
	panicFunc := func(context.Context) error {
		panic(want)
	}

	err := w.WaitForSuccess(ctx, "WaitForSuccess", testWait, panicFunc)
	t.Logf("WaitForSuccess output (not an actual failure):\n%s", err)
	if err == nil {
		t.Fatalf("WaitForSuccess expected an error but returned success")
	}

	if !strings.Contains(err.Error(), want) {
		t.Errorf("WaitForSuccess err did not contain %q", want)
	}
}

// TestWaitForSuccessCutsLongSleep validates that with a long iteration
// interval but short timeout, the function will return based on the shorter
// timeout.
//
// This validates a fix for b/310272841, where WaitForSuccess was waiting
// out the entire interval, even though the overall timeout came much sooner.
func TestWaitForSuccessCutsLongSleep(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// wait uses a short overall timeout, but a long iteration interval.
	wait := Waiting{
		Wait:    100 * time.Millisecond,
		Every:   5 * time.Second,
		Timeout: 100 * time.Millisecond,
	}
	f := func(ctx context.Context) error {
		return errors.New("error")
	}
	ch := make(chan error, 1)
	go func() {
		ch <- w.WaitForSuccess(ctx, "WaitForSuccess", wait, f)
	}()

	wantReturnWithin := time.Second
	select {
	case <-time.After(wantReturnWithin):
		t.Errorf("Test timed out after %s waiting for WaitForSuccess to return based on Waiting.Timeout of %s", wantReturnWithin, wait.Timeout)
	case err := <-ch:
		if err == nil {
			t.Errorf("WaitForSuccess error = %v; want non-nil", err)
		}
	}
}

// This test validates that WaitForSuccess runs the callback function
// within the interval specified by Waiting.Every, even when the callback takes
// significant time to execute.
func TestWaitForSuccessRunsOnConsistentEveryInterval(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	wantIterations := 2
	wait := Waiting{
		Every: 400 * time.Millisecond,
		Wait:  500 * time.Millisecond,
	}

	var iterations int
	f := func(ctx context.Context) error {
		iterations++
		<-ctx.Done()
		return errors.New("error")
	}
	err := w.WaitForSuccess(ctx, "WaitForSucess", wait, f)
	if err == nil {
		t.Fatalf("WaitForSuccess error = %v; want non-nil", err)
	}

	if iterations != wantIterations {
		t.Errorf("With long running callback, WaitForSuccess called func %d times; expected %d iterations", iterations, wantIterations)
	}
}

// This test validates that WaitForSuccess properly retries the callback
// when it returns an error indicating that the context is canceled.
//
// This confirms the desired behavior that our retry logic should not abort
// based on test code which uses its own internal context.
func TestWaitForSuccessRetriesInternalContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	var called int
	f := func(ctx context.Context) error {
		called++
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	err := w.WaitForSuccess(ctx, "WaitForSuccess", testWait, f)
	if err == nil {
		t.Errorf("WaitForSuccess err = %v: expected non-nil", err)
	}
	if called < 2 {
		t.Errorf("With internal context canceled, WaitForSuccess called func %d times; expected 2 or more retries", called)
	}
}

// This test validates that WaitForSuccess properly retries the callback
// when it returns an error indicating that the context has timed out.
//
// This confirms the desired behavior that our retry logic should not abort
// based on test code which uses its own internal context.
func TestWaitForSuccessRetriesInternalContextTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	var called int
	f := func(ctx context.Context) error {
		called++
		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	err := w.WaitForSuccess(ctx, "WaitForSuccess", testWait, f)
	if err == nil {
		t.Errorf("WaitForSuccess err = %v: expected non-nil", err)
	}
	if called < 2 {
		t.Errorf("With internal context timeout, WaitForSuccess called func %d times; expected 2 or more retries", called)
	}
}

// TestRetryOnErrorImmediateSuccessReturnsCorrectCount validates that
// RetryOnError returns a count of 1 when the function succeeds on the
// first try.
func TestRetryOnErrorImmediateSuccessReturnsCorrectCount(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	retry := func(err error) bool { return true }
	f := delaySuccessFunc(0, 0)
	count, err := w.RetryOnError(ctx, testWait, "RetryOnError", retry, f)
	if err != nil {
		t.Fatalf("RetryOnError gave unexpected error: %s", err)
	}

	if count != 1 {
		t.Errorf("RetryOnError count = %d; want 1", count)
	}
}

// TestRetryOnErrorReturnsNoMoreThanMaximumRetainedErrors validates that
// RetryOnError retains up to the maximum number of errors.
func TestRetryOnErrorReturnsNoMoreThanMaximumRetainedErrors(t *testing.T) {
	errMsg := "always fails"
	alwaysFails := func(ctx context.Context) error {
		return errors.New(errMsg)
	}
	currWait := Waiting{
		Wait:    2 * time.Second,
		Every:   100 * time.Millisecond,
		Timeout: 1 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	retry := func(err error) bool { return true }
	count, err := w.RetryOnError(ctx, currWait, "RetryOnError", retry, alwaysFails)

	if count != maxErrsRetained {
		t.Errorf("RetryOnError returned %d errors, but should have returned %d", count, maxErrsRetained)
	}
	if !strings.Contains(err.Error(), errMsg) {
		t.Errorf("RetryOnError error = %q; did not contain %q", err, errMsg)
	}
}

func delaySuccessFunc(initialDelay time.Duration, readyDelay time.Duration) func(context.Context) error {
	var readyTime time.Time
	var once sync.Once
	return func(ctx context.Context) error {
		once.Do(func() {
			readyTime = time.Now().Add(readyDelay)
		})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(initialDelay):
		}

		if time.Now().Before(readyTime) {
			return errors.New("not ready")
		}
		return nil
	}
}

func immediateSuccessFunc(ctx context.Context) error {
	return nil
}

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

package wait

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// A ConditionChecker that checks if a pod is running.
func podRunningChecker(obj client.Object) (bool, error) {
	pod := obj.(*corev1.Pod)
	return pod.Status.Phase == corev1.PodRunning, nil
}

func TestUntil(t *testing.T) {
	fClock := newFakeClock()
	fClient := newFakeClient(t, 2)
	w := &Waiter{
		Client:    fClient,
		Clock:     fClock,
		Logger:    log.Log.WithName("machine-controller-manaager-provider-gdch"),
		ProjectID: "proj1",
	}

	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		// Wait until `clock.After` is called before advancing the
		// clock.
		fClock.afterWG.Wait()

		// Inject an error to ensure the waiter is not stopped prematurely.
		fClient.watchInterface.eventChan <- watch.Event{
			Type: watch.Error,
		}

		// Close the existing watch event channel to verify watcher will reset.
		close(fClient.watchInterface.eventChan)

		// Wait until `client.Watch` is called.
		fClient.watchWG.Wait()

		fClient.watchInterface.eventChan <- watch.Event{
			Object: pod("pod1", "proj1", "Running"),
			Type:   watch.Deleted,
		}

		wg.Done()
	}()

	go func() {
		if err := w.Until(
			context.Background(),
			podRunningChecker,
			"pod1",
			&corev1.Pod{},
			time.Second,
		); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		wg.Done()
	}()

	wg.Wait()
}

func TestUntilErrors(t *testing.T) {
	testCases := map[string]struct {
		action        func(*fakeClock, *fakeClient)
		checker       ConditionChecker
		expectedError string
		initObjs      []client.Object
	}{
		"timeout": {
			action: func(fClock *fakeClock, fClient *fakeClient) {
				// Events with pod phase that is not running should trigger
				// function to return prematurely.
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod1", "proj1", "pending"),
				}

				// Send a time object to the channel to trigger a timeout.
				fClock.timeChan <- time.Now()
			},
			checker:       podRunningChecker,
			expectedError: "timeout, stop the watch",
		},
		"result from a different name": {
			action: func(fClock *fakeClock, fClient *fakeClient) {
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod-x", "proj1", ""),
				}
			},
			checker: podRunningChecker,
			expectedError: "unexpected namespace/name: got 'proj1/pod-x' " +
				"but want 'proj1/pod1'",
		},
		"result from a different namespace": {
			action: func(fClock *fakeClock, fClient *fakeClient) {
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod1", "project-x", ""),
				}
			},
			checker: podRunningChecker,
			expectedError: "unexpected namespace/name: got 'project-x/pod1' " +
				"but want 'proj1/pod1'",
		},
		"checker error": {
			action: func(fClock *fakeClock, fClient *fakeClient) {
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod1", "proj1", ""),
				}
			},
			checker: func(client.Object) (bool, error) {
				return false, errors.New("unknown")
			},
			expectedError: "check object condition: unknown",
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			fClock := newFakeClock()
			fClient := newFakeClient(t, 1, tc.initObjs...)
			w := Waiter{
				Client:    fClient,
				Clock:     fClock,
				ProjectID: "proj1",
			}

			wg := &sync.WaitGroup{}
			wg.Add(2)

			go func() {
				// Wait until `clock.After` and `client.Watch` are called before
				// performing an action.
				fClock.afterWG.Wait()
				fClient.watchWG.Wait()

				tc.action(fClock, fClient)

				wg.Done()
			}()

			go func() {
				if err := w.Until(
					context.Background(),
					tc.checker,
					"pod1",
					&corev1.Pod{},
					time.Second,
				); err == nil {
					t.Error("expecting an error but got nil")
				} else if err.Error() != tc.expectedError {
					t.Errorf("unexpected error message: got %q but want %q",
						err.Error(), tc.expectedError)
				}
				wg.Done()
			}()

			wg.Wait()
		})
	}
}

func TestUntilDeleted(t *testing.T) {
	fClock := newFakeClock()
	fClient := newFakeClient(t, 1)
	w := Waiter{
		Client:    fClient,
		Clock:     fClock,
		ProjectID: "proj1",
	}

	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		// Wait until `clock.After` and `client.Watch` are called before
		// performing an action.
		fClock.afterWG.Wait()
		fClient.watchWG.Wait()

		fClient.watchInterface.eventChan <- watch.Event{
			Object: pod("pod1", "proj1", ""),
			Type:   watch.Deleted,
		}

		wg.Done()
	}()

	go func() {
		if err := w.UntilDeleted(
			context.Background(),
			"pod1",
			&corev1.Pod{},
			time.Second,
		); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		wg.Done()
	}()

	wg.Wait()
}

// Test to ensure that `UntilDeleted` can handle the case when the object is
// already deleted.
func TestUntilDeletedWithNonExistingObject(t *testing.T) {
	fClock := newFakeClock()
	fClient := newFakeClient(t, 1)
	w := Waiter{
		Client:    fClient,
		Clock:     fClock,
		ProjectID: "proj1",
	}

	ctx := context.Background()
	if err := w.UntilDeleted(ctx, "pod1", &corev1.Pod{}, time.Second); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUntilDeletedErrors(t *testing.T) {
	testCases := map[string]struct {
		action        func(*fakeClock, *fakeClient)
		expectedError string
		initObjs      []client.Object
	}{
		"timeout": {
			action: func(fClock *fakeClock, fClient *fakeClient) {
				// Events that are not of type "Deleted" shouldn't trigger
				// anything.
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod1", "proj1", ""),
					Type:   watch.Added,
				}
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod1", "proj1", ""),
					Type:   watch.Modified,
				}
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod1", "proj1", ""),
					Type:   watch.Error,
				}
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod1", "proj1", ""),
					Type:   watch.Bookmark,
				}

				// Send a time object to the channel to trigger a timeout.
				fClock.timeChan <- time.Now()
			},
			expectedError: "timeout, stop the watch",
			initObjs:      []client.Object{pod("pod1", "proj1", "")},
		},
		"result from a different name": {
			action: func(fClock *fakeClock, fClient *fakeClient) {
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod-x", "proj1", ""),
				}
			},
			expectedError: "unexpected namespace/name: got 'proj1/pod-x' " +
				"but want 'proj1/pod1'",
			initObjs: []client.Object{pod("pod1", "proj1", "")},
		},
		"result from a different namespace": {
			action: func(fClock *fakeClock, fClient *fakeClient) {
				fClient.watchInterface.eventChan <- watch.Event{
					Object: pod("pod1", "project-x", ""),
				}
			},
			expectedError: "unexpected namespace/name: got 'project-x/pod1' " +
				"but want 'proj1/pod1'",
			initObjs: []client.Object{pod("pod1", "proj1", "")},
		},
	}

	for id, tc := range testCases {
		t.Run(id, func(t *testing.T) {
			fClock := newFakeClock()
			fClient := newFakeClient(t, 1, tc.initObjs...)
			w := Waiter{
				Client:    fClient,
				Clock:     fClock,
				Logger:    log.Log.WithName("machine-controller-manaager-provider-gdch"),
				ProjectID: "proj1",
			}

			wg := &sync.WaitGroup{}
			wg.Add(2)

			go func() {
				// Wait until `clock.After` and `client.Watch` are called before
				// performing an action.
				fClock.afterWG.Wait()
				fClient.watchWG.Wait()

				tc.action(fClock, fClient)

				wg.Done()
			}()

			go func() {
				if err := w.UntilDeleted(
					context.Background(),
					"pod1",
					&corev1.Pod{},
					time.Second,
				); err == nil {
					t.Error("expecting an error but got nil")
				} else if err.Error() != tc.expectedError {
					t.Errorf("unexpected error message: got %q but want %q",
						err.Error(), tc.expectedError)
				}
				wg.Done()
			}()

			wg.Wait()
		})
	}
}

// Test to ensure that `UntilDeleted` falls back to polling when the watch
// misses the deletion event.
func TestUntilDeletedWithPolling(t *testing.T) {
	fClock := newFakeClock()
	fClient := newFakeClient(t, 1)
	w := Waiter{
		Client:    fClient,
		Clock:     fClock,
		ProjectID: "proj1",
		Logger:    log.Log.WithName("test"),
	}

	podObj := pod("pod1", "proj1", "")
	fClient.Client = fake.NewClientBuilder().WithObjects(podObj).Build()

	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		fClient.watchWG.Wait()

		// Delete object so next Get returns NotFound. But, fake client doesn't
		// send deletion event to the watcher goroutine, mimicking a "hanging"
		// watch. The polling goroutine must catch the deletion.
		err := fClient.Delete(context.Background(), podObj)
		if err != nil {
			t.Errorf("failed to delete pod: %v", err)
		}

		// Trigger ticker
		fClock.tickerChan <- time.Now()

		wg.Done()
	}()

	go func() {
		if err := w.UntilDeleted(
			context.Background(),
			"pod1",
			&corev1.Pod{},
			time.Hour,
		); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		wg.Done()
	}()

	wg.Wait()
}

// Test to ensure that `UntilDeleted` continues polling after a transient error.
func TestUntilDeletedWithPollingError(t *testing.T) {
	fClock := newFakeClock()
	fClient := newFakeClient(t, 1)
	w := Waiter{
		Client:    fClient,
		Clock:     fClock,
		ProjectID: "proj1",
		Logger:    log.Log.WithName("test"),
	}

	podObj := pod("pod1", "proj1", "")
	fClient.Client = fake.NewClientBuilder().WithObjects(podObj).Build()

	wg := &sync.WaitGroup{}
	wg.Add(2)

	go func() {
		fClient.watchWG.Wait()

		// Simulate transient error on next Get
		fClient.getErr = errors.New("transient error")

		// Trigger ticker for first poll (which will fail)
		fClock.tickerChan <- time.Now()

		// Delete object so second poll finds it gone
		err := fClient.Client.Delete(context.Background(), podObj)
		if err != nil {
			t.Errorf("failed to delete pod: %v", err)
		}

		// Trigger ticker for second poll (which should succeed)
		fClock.tickerChan <- time.Now()

		wg.Done()
	}()

	go func() {
		if err := w.UntilDeleted(
			context.Background(),
			"pod1",
			&corev1.Pod{},
			time.Hour,
		); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		wg.Done()
	}()

	wg.Wait()
}

func pod(name, namespace, phase string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Status: corev1.PodStatus{Phase: corev1.PodPhase(phase)},
	}
}

func newFakeClock() *fakeClock {
	fc := &fakeClock{
		timeChan:   make(chan time.Time),
		tickerChan: make(chan time.Time),
	}
	fc.afterWG.Add(1)
	return fc
}

type fakeClock struct {
	clock.Clock

	afterWG    sync.WaitGroup
	timeChan   chan time.Time
	tickerChan chan time.Time
}

func (f *fakeClock) After(duration time.Duration) <-chan time.Time {
	if duration == defaultPollInterval {
		return f.tickerChan
	}
	defer f.afterWG.Done()
	return f.timeChan
}

type watchInterface struct {
	eventChan chan watch.Event
}

func (wi *watchInterface) Stop() {}
func (wi *watchInterface) ResultChan() <-chan watch.Event {
	return wi.eventChan
}

func newFakeClient(t *testing.T, watchCount int, initObjs ...client.Object) *fakeClient {
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatalf("add to scheme: %v", err)
	}

	c := fake.NewClientBuilder().WithObjects(initObjs...).Build()
	fc := &fakeClient{
		Client: c,

		scheme:         s,
		watchInterface: &watchInterface{make(chan watch.Event)},
	}
	fc.watchWG.Add(watchCount)

	return fc
}

type fakeClient struct {
	client.Client

	scheme *runtime.Scheme

	watchWG        sync.WaitGroup
	watchInterface *watchInterface

	getErr error
}

func (fc *fakeClient) Watch(ctx context.Context, obj client.ObjectList,
	opts ...client.ListOption,
) (watch.Interface, error) {
	defer fc.watchWG.Done()

	select {
	// Create a new watcher if the existing eventChan is closed
	case <-fc.watchInterface.eventChan:
		fc.watchInterface = &watchInterface{make(chan watch.Event)}
	default:
	}

	return fc.watchInterface, nil
}

func (fc *fakeClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if fc.getErr != nil {
		err := fc.getErr
		fc.getErr = nil // Clear it for next time to simulate transient error
		return err
	}
	return fc.Client.Get(ctx, key, obj, opts...)
}

func (fc *fakeClient) Scheme() *runtime.Scheme {
	return fc.scheme
}

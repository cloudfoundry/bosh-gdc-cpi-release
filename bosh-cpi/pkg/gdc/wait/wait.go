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

// Package wait provides functions that wait until certain conditions of
// Kubernetes objects are met.
package wait

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

const defaultPollInterval = 5 * time.Minute

type logger interface {
	Info(msg string, keyValues ...any)
}

// Waiter provides methods that wait until certain conditions are met.
type Waiter struct {
	Client    client.WithWatch
	Clock     clock.Clock
	Logger    logger
	ProjectID string
}

// ConditionChecker is a function that checks whether a condition is met for the
// given Kubernetes object.
type ConditionChecker func(obj client.Object) (bool, error)

type result struct {
	err error
}

// Until waits until a condition is firstly met for the object that is
// identified by the `NamespaceName`. It returns a non-nil error when the
// condition is NOT met before the timeout.
func (w *Waiter) Until(
	ctx context.Context,
	check ConditionChecker,
	name string,
	obj client.Object,
	timeout time.Duration,
) error {
	resultCh := make(chan *result)

	ec := func(et watch.EventType, obj client.Object) (bool, error) {
		return check(obj)
	}

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go w.until(childCtx, ec, name, obj, timeout, resultCh)

	result := <-resultCh
	return result.err
}

// UntilDeleted waits until the object identified by the `NamespaceName` is
// deleted. It returns a non-nil error when the object is NOT deleted before the
// timeout.
func (w *Waiter) UntilDeleted(
	ctx context.Context,
	name string,
	obj client.Object,
	timeout time.Duration,
) error {
	ec := func(et watch.EventType, obj client.Object) (bool, error) {
		return et == watch.Deleted, nil
	}

	resultCh := make(chan *result, 2)

	childCtx, cancel := context.WithCancel(ctx)
	// cancel the child context to make sure all goroutines are terminated after
	// this method exits.
	defer cancel()

	pollObj := obj.DeepCopyObject().(client.Object)
	go w.until(childCtx, ec, name, obj, timeout, resultCh)
	go w.checkDeleted(childCtx, name, pollObj, resultCh)

	result := <-resultCh
	return result.err
}

func (w *Waiter) checkDeleted(
	ctx context.Context,
	name string,
	obj client.Object,
	resultCh chan *result,
) {
	key := client.ObjectKey{
		Name:      name,
		Namespace: w.ProjectID,
	}

	check := func() (bool, error) {
		err := w.Client.Get(ctx, key, obj)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}

	if deleted, err := check(); deleted {
		resultCh <- &result{}
		return
	} else if err != nil {
		w.Logger.Info("waiter", "initial check failed", err)
	}

	// Poll every 5 minutes
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.Clock.After(defaultPollInterval):
			if deleted, err := check(); deleted {
				resultCh <- &result{}
				return
			} else if err != nil {
				w.Logger.Info("waiter", "poll failed", err)
			}
		}
	}
}

type eventChecker func(watch.EventType, client.Object) (bool, error)

func (w *Waiter) until(
	ctx context.Context,
	check eventChecker,
	name string,
	obj client.Object,
	timeout time.Duration,
	resultCh chan *result,
) {
	ol, err := w.toObjectList(obj)
	if err != nil {
		resultCh <- &result{
			err: fmt.Errorf("convert Object %v to ObjectList: %w", obj, err),
		}
		return
	}

	timeoutChan := w.Clock.After(timeout)
	for {
		stopWait, err := w.wait(ctx, check, name, ol, timeoutChan)
		if stopWait {
			resultCh <- &result{
				err: err,
			}
			return
		}

		w.Logger.Info("waiter", "continue the wait without terminating prematurely")
		continue
	}
}

// When `wait` returns true, it signals to wait wrapper `until` to terminate the wait, otherwise, continue waiting
func (w *Waiter) wait(
	ctx context.Context,
	check eventChecker,
	name string,
	objList client.ObjectList,
	timeoutChan <-chan time.Time,
) (bool, error) {
	namespace := w.ProjectID
	set := fields.Set{"metadata.name": name}
	listOpt := &client.ListOptions{
		FieldSelector: set.AsSelector(),
		Namespace:     namespace,
	}

	wi, err := w.Client.Watch(ctx, objList, listOpt)
	if err != nil {
		return true, fmt.Errorf("set up watch: %v", err)
	}
	defer wi.Stop()

	for {
		select {
		case <-timeoutChan:
			return true, errors.New("timeout, stop the watch")
		case res, ok := <-wi.ResultChan():
			if !ok {
				w.Logger.Info("waiter", "current watcher channel is closed")

				return false, nil
			}

			if res.Type == watch.Error {
				w.Logger.Info("waiter", "got an error but keep waiting", res)

				// Ignore errors to avoid stopping the waiter prematurely.
				continue
			}

			obj, ok := res.Object.(client.Object)
			if !ok {
				return true, fmt.Errorf(
					"result.Object does not implement `client.Object`: %v",
					res.Object)
			}

			if obj.GetName() != name || obj.GetNamespace() != namespace {
				return true, fmt.Errorf(
					"unexpected namespace/name: got '%s/%s' but want '%s/%s'",
					obj.GetNamespace(), obj.GetName(), namespace, name)
			}

			switch ok, err := check(res.Type, obj); {
			case err != nil:
				return true, fmt.Errorf("check object condition: %v", err)
			case ok:
				return true, nil
			}
		}
	}
}

func (w *Waiter) toObjectList(o client.Object) (client.ObjectList, error) {
	scheme := w.Client.Scheme()
	gvk, err := apiutil.GVKForObject(o, scheme)
	if err != nil {
		return nil, fmt.Errorf("look up GVK for object %v: %w", o, err)
	}
	// HACKHACK: append "List" to the object kind to get the kind of the object
	// list.
	listGVK := gvk.GroupVersion().WithKind(gvk.Kind + "List")
	l, err := scheme.New(listGVK)
	if err != nil {
		return nil, fmt.Errorf("create new object: %v", err)
	}

	ol, ok := l.(client.ObjectList)
	if !ok {
		return nil, fmt.Errorf("convert object to object list")
	}

	return ol, nil
}

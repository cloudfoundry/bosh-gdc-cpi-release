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

package mock

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/gdc/wait"
)

type serverSideAction func(context.Context, client.Client) error

type MockWaiter struct {
	Client        client.Client
	Err           error
	TargetObjects map[string]client.Object
	ProjectID     string

	// serverSideAction is a function that is invoked in `Until` or
	// `UntilDeleted` to change objects via the client. This allows us to mutate
	// the "server-side" status before a condition checker is evaluated.
	ServerSideActions map[string]serverSideAction

	// WaitForSuccessFunc allows you to provide a custom mock implementation for
	// the WaitForSuccess method.
	WaitForSuccessFunc func(ctx context.Context, msg string, timeout wait.Waiting, check func(context.Context) error) error
}

func NewMockWaiter(
	c client.Client,
	projectID string,
) *MockWaiter {
	return &MockWaiter{
		Client:            c,
		ProjectID:         projectID,
		ServerSideActions: make(map[string]serverSideAction),
		TargetObjects:     make(map[string]client.Object),
	}
}

func (m *MockWaiter) Until(
	ctx context.Context, check wait.ConditionChecker,
	name string, obj client.Object, timeout time.Duration,
) error {
	gvks, _, _ := m.Client.Scheme().ObjectKinds(obj)
	if action, ok := m.ServerSideActions[gvks[0].Kind]; ok {
		if err := action(ctx, m.Client); err != nil {
			return fmt.Errorf("service side action: %w", err)
		}
	}

	obj, ok := m.TargetObjects[gvks[0].Kind]
	if !ok {
		return m.Err
	}

	nn := types.NamespacedName{
		Name:      name,
		Namespace: m.ProjectID,
	}
	if err := m.Client.Get(ctx, nn, obj); err != nil {
		return fmt.Errorf("get %v: %v", nn, err)
	}
	if ok, err := check(obj); !ok || err != nil {
		return fmt.Errorf("check() = %t, %v", ok, err)
	}

	return nil
}

func (m *MockWaiter) UntilDeleted(
	ctx context.Context, name string, obj client.Object,
	timeout time.Duration,
) error {
	gvks, _, _ := m.Client.Scheme().ObjectKinds(obj)
	if action, ok := m.ServerSideActions[gvks[0].Kind]; ok {
		if err := action(ctx, m.Client); err != nil {
			return fmt.Errorf("service side action: %w", err)
		}
	}

	obj, ok := m.TargetObjects[gvks[0].Kind]
	if !ok {
		return m.Err
	}

	nn := types.NamespacedName{
		Name:      name,
		Namespace: m.ProjectID,
	}
	if err := m.Client.Get(ctx, nn, obj); err == nil || !errors.IsNotFound(err) {
		return fmt.Errorf("object %q is not deleted: %w", name, err)
	}

	return nil
}

func (m *MockWaiter) WaitForSuccess(
	ctx context.Context, msg string, timeout wait.Waiting,
	check func(context.Context) error,
) error {
	if m.WaitForSuccessFunc != nil {
		return m.WaitForSuccessFunc(ctx, msg, timeout, check)
	}
	if err := check(ctx); err != nil {
		return err
	}

	return nil
}

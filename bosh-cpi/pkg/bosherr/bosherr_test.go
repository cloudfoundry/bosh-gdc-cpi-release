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

package bosherr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cloudfoundry/bosh-cpi-go/rpc"
)

func TestDefaultErrType(t *testing.T) {
	err := Err{}
	actual := err.Type()
	expected := string(BoshErrCloudErr)
	if actual != expected {
		t.Fatalf("default error type should be %v, got: %v", expected, actual)
	}
}

func TestEmptyErrNoPanic(t *testing.T) {
	err := Err{}

	// if any of these panic it will fail the test and report which paniced
	_ = err.Type()
	_ = err.Error()
	_ = err.CanRetry()
	_ = err.Unwrap()
}

func TestErrUnwrapping(t *testing.T) {
	insideError := errors.New("inside error")

	err := Err{
		Message: fmt.Errorf("outside error: %w", insideError),
	}

	if !errors.Is(err, insideError) {
		t.Fatal("insideError was unable to be unwrapped from the error wrapped in Message")
	}
}

func TestWrapfPreservesRetryableForTheDispatcher(t *testing.T) {
	// bosh-cpi-go type-asserts the top-level error rather than unwrapping it, so
	// a retryable Err must still be the outermost type after annotation.
	conflict := Err{Message: errors.New("patch VM: conflict"), Retryable: true}

	err := Wrapf(Wrapf(error(conflict), "stop VM %s", "vm1"), "attach disk %s", "disk1")

	retryable, ok := err.(rpc.RetryableError)
	if !ok {
		t.Fatalf("got %T, expected the top-level error to implement rpc.RetryableError", err)
	}
	if !retryable.CanRetry() {
		t.Error("got CanRetry false, expected the retryable hint to survive wrapping")
	}

	expected := "attach disk disk1: stop VM vm1: patch VM: conflict"
	if err.Error() != expected {
		t.Errorf("got message %q, expected %q", err.Error(), expected)
	}
}

func TestWrapfLeavesPlainErrorsAlone(t *testing.T) {
	inside := errors.New("boom")

	err := Wrapf(inside, "stop VM %s", "vm1")

	if _, ok := err.(rpc.RetryableError); ok {
		t.Error("a plain error must not be promoted to a retryable Err")
	}
	if !errors.Is(err, inside) {
		t.Error("the original error must remain unwrappable")
	}
	if err.Error() != "stop VM vm1: boom" {
		t.Errorf("got message %q, expected %q", err.Error(), "stop VM vm1: boom")
	}
}

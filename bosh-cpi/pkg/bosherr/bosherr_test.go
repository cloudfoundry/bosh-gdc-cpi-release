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

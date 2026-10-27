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

import "github.com/cloudfoundry/bosh-cpi-go/rpc"

type BoshErrType string

// Err communicates extra error information to the bosh-cpi-go library
// The link below provides context for how this struct is parsed by bosh-cpi-go.
// https://github.com/cloudfoundry/bosh-cpi-go/blob/27f6ca08ad1b1bfef72b537169aea3fc06645c83/rpc/json_dispatcher.go#L95
type Err struct {
	// Message is an error that gets converted to a string and sent to Bosh with the message key.
	// If you are returning a standard golang error to the CPI library,
	// using this struct with only this field is functionally equivalent.
	//
	// Zero/Default value is "" (empty string).
	Message error

	// ErrType allows you to provide a specific type of error to Bosh.
	//
	// Zero/Default value is "Bosh::Clouds::CloudError".
	//
	// An example of where this is useful is https://bosh.io/docs/cpi-api-v2-method/resize-disk/
	// to communicate why disk resizing failed or if it is unsupported.
	ErrType BoshErrType

	// Setting Retryable to true will let the CLI/Director using this CPI
	// that it can safely call the method again to retry.
	//
	// Zero/Default value is false.
	//
	// An example of this could be when reading, mutating, then updating a GDCH resource manifest
	// If a controller or user makes a change to the resource after we read but before updating, the update will
	// fail with a conflict error. In this case, the only way to fix the error is with a retry.
	Retryable bool
}

var (
	_ rpc.CloudError     = Err{}
	_ rpc.RetryableError = Err{}
)

const (
	BoshErrCloudErr     BoshErrType = "Bosh::Clouds::CloudError"
	BoshErrNotSupported BoshErrType = "Bosh::Clouds::NotSupported"
	// Add more BoshErrTypes when needed. Please refer to:
	// https://github.com/cloudfoundry/bosh-cpi-ruby/blob/master/lib/cloud/errors.rb
)

func (e Err) Error() string {
	if e.Message == nil {
		return ""
	}
	return e.Message.Error()
}

func (e Err) Unwrap() error {
	if e.Message == nil {
		return nil
	}

	if unwrapableErr, ok := e.Message.(interface{ Unwrap() error }); ok {
		return unwrapableErr.Unwrap()
	}

	return nil
}

func (e Err) Type() string {
	if e.ErrType == "" {
		return string(BoshErrCloudErr)
	}
	return string(e.ErrType)
}

func (e Err) CanRetry() bool {
	return e.Retryable
}

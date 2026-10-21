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

package util

import (
	"os"
	"testing"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	"github.com/google/go-cmp/cmp"
)

var (
	logger           = cfboshlog.New(boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr))
	longStr          = "abvxcnbhteafuximxetbxvuyddcghlnqmkwlvebchkfuyyuybbaepbzgpdcposlnz"
	longStrWithSlash = "a/bv/xcnbhteafuximxetbxvuyddcghlnqmkwlvebchkfuyyuybbaepbzgpdcposlnz"
)

func TestPatchMetadata(t *testing.T) {
	metadata := map[string]string{
		"time":                            "2024-01-04T16:03:38Z",
		"time_iso":                        "21000104T100338Z",
		"with_slash":                      "a/b",
		"without_slash":                   "ab",
		"with_length_violation":           longStr,
		"with_length_violation_and_slash": longStrWithSlash,
	}

	expectedMatadata := map[string]string{
		"time":                            "20240104T160338Z",
		"time_iso":                        "21000104T100338Z",
		"with_slash":                      "a--b",
		"without_slash":                   "ab",
		"with_length_violation":           longStr[0:maxLen],
		"with_length_violation_and_slash": "a--bv--xcnbhteafuximxetbxvuyddcghlnqmkwlvebchkfuyyuybbaepbzgpdc",
	}

	PatchMetadata(metadata, logger)
	if diff := cmp.Diff(expectedMatadata, metadata); diff != "" {
		t.Fatalf("test requests diff (-want +got):\n%s", diff)
	}
}

func TestGetMetadataWithLengthViolation(t *testing.T) {
	metadata := map[string]string{
		"time":                            "2024-01-04T16:03:38Z",
		"time_iso":                        "21000104T100338Z",
		"with_slash":                      "a/b",
		"without_slash":                   "ab",
		"with_length_violation":           longStr,
		"with_length_violation_and_slash": longStrWithSlash,
	}

	expectedMatadata := map[string]string{
		"with_length_violation":           longStr,
		"with_length_violation_and_slash": longStrWithSlash,
	}

	gotMetadata := GetMetadataWithLengthViolation(metadata)
	if diff := cmp.Diff(expectedMatadata, gotMetadata); diff != "" {
		t.Fatalf("test requests diff (-want +got):\n%s", diff)
	}
}

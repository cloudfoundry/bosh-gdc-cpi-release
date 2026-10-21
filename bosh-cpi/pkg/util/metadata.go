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
	"regexp"
	"strings"
	"time"

	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
)

const (
	ISO8601BasicFormat = "20060102T150405Z"
	maxLen             = 63
)

// PatchMetadata patches metadata map to avoild invalid characters before populating as kubernetes labels.
// Kubernetes label value only accepts alphanumeric, '-', '_', and '.' characters, see details in
// https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/#syntax-and-character-set.
func PatchMetadata(metadataMap map[string]string, logger cfboshlog.Logger) {
	for k, v := range metadataMap {
		regex := regexp.MustCompile(`[:/]`)
		if !regex.MatchString(v) && len(v) <= maxLen {
			// all characters are valid, no need to patch
			continue
		}

		// BOSH set timestamp in RFC3339 format which includes ':' character, e.g.,
		// https://github.com/cloudfoundry/bosh-cli/blob/main/deployment/vm/manager.go#L127.
		// Convert to ISO 8601 basic time format if the string is a RFC3339 formated timestamp
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			metadataMap[k] = t.Format(ISO8601BasicFormat)
			logger.Warn("PatchMetadata", "patched timestamp value of key %q from %v to %v", k, v, metadataMap[k])
			continue
		}

		// for non-timestamp string, patch the string by repliacing the invalid character '/' to valid substring "--"
		metadataMap[k] = strings.ReplaceAll(v, "/", "--")
		logger.Warn("PatchMetadata", "patched key %q from %v to %v", k, v, metadataMap[k])

		// Kubernetes label value must be 63 characters or less. Trim values with length violation to
		// max supported length
		modifiedV := metadataMap[k]
		if len(modifiedV) > maxLen {
			metadataMap[k] = modifiedV[0:maxLen]
		}
	}
}

// GetMetadataWithLengthViolation gets metadata whose values have length violation against kubernetes
// labels. Those filtered metadata can be used as a backup, i.e puplated to kubernetes annotations
// which does not have such length restriction
func GetMetadataWithLengthViolation(metadataMap map[string]string) map[string]string {
	filteredMetadataMap := make(map[string]string)
	for k, v := range metadataMap {
		// Includes the length added by the patch
		tmpv := strings.ReplaceAll(v, "/", "--")

		if len(tmpv) > maxLen {
			filteredMetadataMap[k] = v
		}
	}

	return filteredMetadataMap
}

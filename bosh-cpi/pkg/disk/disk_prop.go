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

package disk

import (
	"errors"
	"fmt"

	consts "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/consts"
	"github.com/hashicorp/go-multierror"
)

type Property struct {
	// Zone in which disk should be created.
	Zone string `json:"zone,omitempty"`
	// DiskType should either be Performance (for high IOPS storage class), Standard, or left empty (defaults to Standard).
	DiskType string `json:"diskType,omitempty"`
}

func (p *Property) validate() error {
	var errs *multierror.Error

	if len(p.DiskType) > 0 {
		if p.DiskType != consts.DiskTypePerformance && p.DiskType != consts.DiskTypeStandard {
			errs = multierror.Append(errs, fmt.Errorf("disk type must be either '%v' or '%v'", consts.DiskTypePerformance, consts.DiskTypeStandard))
		}
	} else if len(p.DiskType) == 0 {
		// Empty/missing disk type defaults to Standard disk type.
		p.DiskType = consts.DiskTypeStandard
	}

	return errs.ErrorOrNil()
}

func (p *Property) multizoneValidate() error {
	var errs *multierror.Error

	if len(p.Zone) == 0 {
		errs = multierror.Append(errs, errors.New("zone name must not be empty"))
	}

	return errs.ErrorOrNil()
}

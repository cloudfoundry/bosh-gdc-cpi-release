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

package stemcell

import (
	"errors"

	"github.com/hashicorp/go-multierror"
)

// Selected properties from cloud properties hash extracted from the stemcell tarball
// https://bosh.io/docs/cpi-api-v2-method/create-stemcell/#arguments
type Property struct {
	Name           string `json:"name,omitempty"`
	Version        string `json:"version,omitempty"`
	Infrastructure string `json:"infrastructure,omitempty"`
	Hypervisor     string `json:"hypervisor,omitempty"`
	OsVersion      string `json:"os_version,omitempty"`
}

func (p *Property) validate() error {
	var errs *multierror.Error

	if len(p.Name) == 0 {
		errs = multierror.Append(errs, errors.New("must provide non-empty Name"))
	}
	if len(p.Version) == 0 {
		errs = multierror.Append(errs, errors.New("must provide non-empty Version"))
	}
	if p.Infrastructure != "gdch" {
		errs = multierror.Append(errs, errors.New("Infrastruction must be 'gdch'"))
	}
	if p.Hypervisor != "kvm" {
		errs = multierror.Append(errs, errors.New("Hypervisor must be 'kvm'"))
	}

	return errs.ErrorOrNil()
}

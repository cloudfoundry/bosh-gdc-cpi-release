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

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/cloudfoundry/bosh-cpi-go/apiv1"
	"github.com/cloudfoundry/bosh-cpi-go/rpc"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/factory"
)

type cpiExecutor struct {
	rpcFactory *rpc.Factory
	cpiFactory *factory.Factory
	ctx        *apiv1.CloudPropsImpl
}

func (ce *cpiExecutor) exec(request *rpc.Request) ([]byte, error) {
	request.Context = *ce.ctx
	requestByte, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	var in, out bytes.Buffer
	in.Write(requestByte)
	cli := ce.rpcFactory.NewCLIWithInOut(&in, &out, ce.cpiFactory)
	if err := cli.ServeOnce(); err != nil {
		return nil, err
	}

	var response []byte
	if response, err = io.ReadAll(&out); err != nil {
		return nil, err
	}

	var boshResponse rpc.Response
	if err := json.Unmarshal(response, &boshResponse); err != nil {
		return nil, err
	}

	if boshResponse.Error != nil {
		return nil, fmt.Errorf("%s:%s", boshResponse.Error.Type, boshResponse.Error.Message)
	}

	jsonData, err := json.Marshal(boshResponse.Result)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	return jsonData, nil
}

func getRequest(method string, args []interface{}) *rpc.Request {
	// Argument cannot be nil:
	// https://github.com/cloudfoundry/bosh-cpi-go/blob/master/rpc/json_dispatcher.go#L65
	if args == nil {
		args = make([]interface{}, 0)
	}

	return &rpc.Request{
		Method:     method,
		Arguments:  args,
		APIVersion: 2,
	}
}

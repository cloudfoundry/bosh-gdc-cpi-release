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

package main

import (
	"flag"
	"os"

	"github.com/cloudfoundry/bosh-cpi-go/rpc"
	cfboshlog "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/logger"
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
	"github.com/cloudfoundry/bosh-utils/system"
	_ "github.com/cloudfoundry/bosh-utils/uuid"

	kubeclient "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/client/kube"
	cpiconfig "github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/factory"
)

var (
	configPathOpt = flag.String("configPath", "", "Path to configuration file")
)

func main() {
	logger := boshlog.NewWriterLogger(boshlog.LevelDebug, os.Stderr)
	fs := system.NewOsFileSystem(boshlog.NewWriterLogger(boshlog.LevelInfo, os.Stderr))
	defer logger.HandlePanic("Main")

	flag.Parse()

	config, err := cpiconfig.NewConfigFromPath(*configPathOpt, fs)
	if err != nil {
		logger.Error("main", "Loading config %s", err.Error())
		os.Exit(1)
	}

	cloudClient, err := kubeclient.GetKubeClient(&config.GDC, "")
	if err != nil {
		logger.Error("main", "get client %s", err.Error())
		os.Exit(1)
	}

	logger.Info("main", "Authentication succeeded")

	boshLoggerWrapper := cfboshlog.New(logger)
	cpiFactory := factory.NewFactory(cloudClient, config.Agent, config, boshLoggerWrapper)

	cli := rpc.NewFactory(logger).NewCLI(cpiFactory)

	logger.Info("main", "Starting service...")
	if err = cli.ServeOnce(); err != nil {
		logger.Error("main", "Serving once: %s", err)
		os.Exit(1)
	}
}

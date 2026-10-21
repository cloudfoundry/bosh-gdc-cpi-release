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

package logger

import (
	boshlog "github.com/cloudfoundry/bosh-utils/logger"
)

type Logger interface {
	Info(tag string, keyAndValues ...any)
	Debug(tag, msg string, args ...interface{})
	Warn(tag, msg string, args ...interface{})
	Error(tag, msg string, args ...interface{})
}

type boshLoggerWrapper struct {
	logger boshlog.Logger
}

func New(logger boshlog.Logger) Logger {
	return &boshLoggerWrapper{logger}
}

func (l *boshLoggerWrapper) Info(tag string, keyAndValues ...any) {
	if len(keyAndValues) == 0 {
		l.logger.Info(tag, "")
		return
	}
	key, ok := keyAndValues[0].(string)
	if !ok {
		l.logger.Info(tag, "", keyAndValues...)
		return
	}
	l.logger.Info(tag, key, keyAndValues[1:]...)
}

func (l *boshLoggerWrapper) Debug(tag, msg string, args ...interface{}) {
	l.logger.Debug(tag, msg, args...)
}

func (l *boshLoggerWrapper) Warn(tag, msg string, args ...interface{}) {
	l.logger.Warn(tag, msg, args...)
}

func (l *boshLoggerWrapper) Error(tag, msg string, args ...interface{}) {
	l.logger.Error(tag, msg, args...)
}

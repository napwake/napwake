/*
Copyright 2026 The Napwake Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const rendered = `
apiVersion: v1
kind: Service
metadata:
  name: napwake-metrics
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: napwake
spec:
  template:
    spec:
      containers:
        - name: manager
          command: [/manager]
          args: ["--timezone=UTC", "--default-pause-early=<nil>"]
        - name: sidecar
          command: [/proxy]
          args: ["--port=1"]
---
apiVersion: batch/v1
kind: Job
metadata:
  name: napwake-uninstall-guard
spec:
  template:
    spec:
      containers:
        - name: guard
          command: [/manager]
          args: ["--guard=uninstall"]
`

func TestOperatorContainersFindsEveryManagerContainer(t *testing.T) {
	containers, err := operatorContainers([]byte(rendered))
	require.NoError(t, err)
	assert.Equal(t, []container{
		{source: "Deployment/napwake container manager", args: []string{"--timezone=UTC", "--default-pause-early=<nil>"}},
		{source: "Job/napwake-uninstall-guard container guard", args: []string{"--guard=uninstall"}},
	}, containers)
}

func TestCheckReportsRefusedArguments(t *testing.T) {
	refuseNil := func(args []string) error {
		if slices.ContainsFunc(args, func(a string) bool { return a == "--default-pause-early=<nil>" }) {
			return errors.New(`invalid value "<nil>" for flag -default-pause-early`)
		}
		return nil
	}
	err := check([]byte(rendered), refuseNil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Deployment/napwake container manager")
	assert.NotContains(t, err.Error(), "uninstall-guard")

	assert.NoError(t, check([]byte(rendered), func([]string) error { return nil }))
}

func TestCheckFailsWithNoOperatorContainer(t *testing.T) {
	assert.Error(t, check([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"),
		func([]string) error { return nil }))
}

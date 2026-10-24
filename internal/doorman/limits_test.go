/*
Copyright 2026.

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

package doorman

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NapwakeDoormanNearCapacity warns as a doorman pod nears the caps past
// which it refuses connections, so its rules, in the chart and in
// config/prometheus, divide by the caps the doorman has.
func TestNearCapacityAlert_DividesByTheCaps(t *testing.T) {
	for _, path := range []string{
		filepath.Join("config", "prometheus", "alerts.yaml"),
		filepath.Join("charts", "napwake", "templates", "prometheusrule.yaml"),
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", path))
		require.NoError(t, err)
		rules := string(raw)
		assert.Contains(t, rules, fmt.Sprintf("napwake_doorman_held_connections / %d,", defaultLimits.maxHeld), path)
		assert.Contains(t, rules, fmt.Sprintf("napwake_doorman_proxied_connections / %d,", defaultLimits.maxProxied), path)
	}
}

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

package nodescale

import (
	"context"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// reportEvery is how often the operator looks again. Autoscalers are
// installed and removed rarely, and each look is a handful of reads.
const reportEvery = 10 * time.Minute

// Reporter says which node autoscaler the operator found: in its log, when
// that changes, and through publish. What pausing frees comes off the bill
// only as nodes scale down, so a cluster with nothing scaling them is the
// first thing to know when freed and saved differ.
type Reporter struct {
	reader  client.Reader
	publish func(kinds []string)
	every   time.Duration
}

// NewReporter returns a Reporter that reads through reader and hands each
// look's kinds to publish: the autoscalers found, or "none" or "unknown".
func NewReporter(reader client.Reader, publish func(kinds []string)) *Reporter {
	return &Reporter{reader: reader, publish: publish, every: reportEvery}
}

// NeedLeaderElection keeps the report to the leader, whose log and metrics
// are the ones read.
func (r *Reporter) NeedLeaderElection() bool { return true }

// Start looks at once, then every r.every until ctx ends.
func (r *Reporter) Start(ctx context.Context) error {
	var last string
	ticker := time.NewTicker(r.every)
	defer ticker.Stop()
	for {
		last = r.report(ctx, last)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Reporter) report(ctx context.Context, last string) string {
	logger := log.FromContext(ctx)
	a, err := Detect(ctx, r.reader)
	if err != nil {
		logger.Error(err, "detecting the node autoscaler")
		return last
	}
	kinds := Kinds(a)
	r.publish(kinds)
	key := strings.Join(kinds, ",")
	if key == last {
		return last
	}
	switch kinds[0] {
	case "none":
		logger.Info("no node autoscaler found: what pausing frees won't come off the bill until something "+
			"scales nodes down", "docs", "https://docs.napwake.dev/guides/cluster-autoscaler/")
	case "unknown":
		logger.Info("couldn't tell which node autoscaler runs", "unreadable", strings.Join(a.Unread, ", "))
	default:
		logger.Info("node autoscaler found", "kinds", key)
	}
	return key
}

// Kinds are the kinds of autoscaler a found, sorted, or "none" when it read
// everywhere it looks and found none, or "unknown" when it couldn't.
func Kinds(a Autoscaler) []string {
	if len(a.Scalers) == 0 {
		if a.Known() {
			return []string{"none"}
		}
		return []string{"unknown"}
	}
	kinds := make([]string, 0, len(a.Scalers))
	for _, s := range a.Scalers {
		if !slices.Contains(kinds, string(s.Kind)) {
			kinds = append(kinds, string(s.Kind))
		}
	}
	slices.Sort(kinds)
	return kinds
}

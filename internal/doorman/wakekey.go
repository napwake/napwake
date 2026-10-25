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

package doorman

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// WakeKeyField is the field of the wake key Secret that holds the key.
	WakeKeyField = "key"

	// wakeKeyRefresh is how often the shared key is read again, so a key
	// changed by emptying the Secret reaches every replica.
	wakeKeyRefresh = time.Minute
)

// watchWakeKey keeps the key tokens are signed with the one in the wake key
// Secret, every wakeKeyRefresh until ctx is done. A replica that can't
// read it goes on with the key it has, its own until it has read one: a
// click then wakes the workload only when it reaches the replica that
// showed the page.
func (s *Server) watchWakeKey(ctx context.Context) {
	ticker := time.NewTicker(wakeKeyRefresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.refreshWakeKey(ctx)
	}
}

func (s *Server) refreshWakeKey(ctx context.Context) {
	loadCtx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	if err := s.loadWakeKey(loadCtx); err != nil && ctx.Err() == nil {
		log.FromContext(ctx).WithName("doorman").Error(err, "loading the key the Wake it button is signed with",
			"secret", s.wakeKeySecret.String())
	}
}

// loadWakeKey uses the key in the wake key Secret, and fills an empty one
// with a new random key. Two replicas filling it at once conflict, and the
// one that loses uses the winner's.
func (s *Server) loadWakeKey(ctx context.Context) error {
	for range 2 {
		var secret corev1.Secret
		if err := s.apiReader.Get(ctx, s.wakeKeySecret, &secret); err != nil {
			return fmt.Errorf("reading the wake key secret: %w", err)
		}
		if key := secret.Data[WakeKeyField]; len(key) > 0 {
			if len(key) < wakeKeySize {
				return fmt.Errorf("the wake key secret's %q is %d bytes, fewer than the %d needed", WakeKeyField,
					len(key), wakeKeySize)
			}
			s.wakeKeys.use(key, s.now())
			return nil
		}
		key := newWakeKey()
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[WakeKeyField] = key
		err := s.client.Update(ctx, &secret)
		if apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("writing the wake key secret: %w", err)
		}
		s.wakeKeys.use(key, s.now())
		return nil
	}
	return fmt.Errorf("the wake key secret %s kept changing while being filled", s.wakeKeySecret)
}

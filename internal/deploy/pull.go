// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import (
	"context"
	"strings"
	"time"
)

// pullWaits are the pauses between tries when a registry says to slow down.
var pullWaits = []time.Duration{5 * time.Second, 20 * time.Second, time.Minute}

// rateLimited says whether a pull failed only because of the registry's limits.
func rateLimited(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"retry-after", "toomanyrequests", "too many requests", "429"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// pullImage pulls an image, waiting when a registry's rate limit is the only trouble.
func pullImage(ctx context.Context, r Runner, ref string) error {
	_, err := r.Run(ctx, "", "pull", "--quiet", ref)
	for _, wait := range pullWaits {
		if !rateLimited(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		_, err = r.Run(ctx, "", "pull", "--quiet", ref)
	}
	return err
}

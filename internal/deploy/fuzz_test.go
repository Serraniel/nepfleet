// SPDX-License-Identifier: AGPL-3.0-or-later

package deploy

import "testing"

// Plans compare compose files written by users and templates: diffs must
// never panic, and a file compared with itself loses nothing.
func FuzzServiceDiff(f *testing.F) {
	f.Add([]byte("services:\n  web: {image: \"nginx:${TAG:-1}\"}\nvolumes: {data: {}}\n"), []byte("services: {}\n"), []byte("TAG='2'\n"))
	f.Add([]byte("services: [1]"), []byte("x: [}"), []byte("A='$${B}'\n"))
	f.Add([]byte("services:\n  a: &x {image: a}\n  b: *x\nnetworks: {n: null}\n"), []byte("services:\n  a: {image: \"${A:-$$}\"}\n"), []byte(""))
	f.Fuzz(func(t *testing.T, oldCompose, newCompose, env []byte) {
		_ = destructive(oldCompose, newCompose)
		if gone := destructive(newCompose, newCompose); len(gone) > 0 {
			t.Fatalf("a file compared with itself removes %v", gone)
		}
		_ = services(oldCompose, env)
		_ = services(newCompose, env)
	})
}

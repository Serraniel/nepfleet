// SPDX-License-Identifier: AGPL-3.0-or-later

package fleet

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
)

// Storage is network storage stacks can mount as Docker volumes, e.g. a NAS
// share or a Hetzner Storage Box (fleet.yaml spec.storage.<name>).
type Storage struct {
	// Type is cifs (SMB, Windows and most NAS) or nfs.
	Type string `yaml:"type" schema:"required;enum=cifs|nfs"`
	// Address is the server's host name or IP address.
	Address string `yaml:"address" schema:"required"`
	// Share is the SMB share name (cifs) or the exported path (nfs).
	Share string `yaml:"share" schema:"required"`
	// Username for cifs.
	Username string `yaml:"username"`
	// PasswordSecret names the key in fleet.secret.yaml holding the password
	// (cifs).
	PasswordSecret string `yaml:"passwordSecret"`
	// Options are extra mount options, e.g. vers=3.1.1, uid=1000, nfsvers=4.
	Options []string `yaml:"options"`
}

// VolumeMount points a stack volume at network storage.
type VolumeMount struct {
	// Storage names a fleet.yaml spec.storage entry.
	Storage string `yaml:"storage" schema:"required"`
	// Path is a folder inside the share (empty = the share itself).
	Path string `yaml:"path"`
}

var (
	storageAddrRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$|^\[[0-9a-fA-F:]+\]$`)
	mountOptRe    = regexp.MustCompile(`^[a-z0-9_]+(=[A-Za-z0-9_.:/-]+)?$`)
	sharePathRe   = regexp.MustCompile(`^[A-Za-z0-9 _.$/-]+$`)
)

func checkStorage(f *Fleet) error {
	for _, name := range slices.Sorted(maps.Keys(f.Spec.Storage)) {
		s := f.Spec.Storage[name]
		bad := func(format string, a ...any) error {
			return fmt.Errorf("fleet.yaml: spec.storage.%s: %s", name, fmt.Sprintf(format, a...))
		}
		switch {
		case !nameRe.MatchString(name):
			return bad("a name is lowercase letters, digits, - and _")
		case s.Type != "cifs" && s.Type != "nfs":
			return bad("type is cifs or nfs")
		case !storageAddrRe.MatchString(s.Address):
			return bad("address %q is not a host name or IP address", s.Address)
		case !sharePathRe.MatchString(s.Share) || strings.Contains(s.Share, ".."):
			return bad("share %q is not a share name or path", s.Share)
		case s.Type == "nfs" && !strings.HasPrefix(s.Share, "/"):
			return bad("an nfs share is the exported path, starting with /")
		case s.Type == "nfs" && (s.Username != "" || s.PasswordSecret != ""):
			return bad("nfs has no username or password")
		}
		for _, o := range s.Options {
			// Credentials go through passwordSecret, never options.
			if !mountOptRe.MatchString(o) || strings.HasPrefix(o, "password=") || strings.HasPrefix(o, "username=") || strings.HasPrefix(o, "addr=") {
				return bad("option %q is not a plain mount option (credentials go in username and passwordSecret)", o)
			}
		}
	}
	return nil
}

// CheckVolumeMounts validates a stack's spec.volumes against the fleet's storage.
func CheckVolumeMounts(f *Fleet, s *Stack) error {
	for _, key := range slices.Sorted(maps.Keys(s.Spec.Volumes)) {
		m := s.Spec.Volumes[key]
		if _, ok := f.Spec.Storage[m.Storage]; !ok {
			return fmt.Errorf("spec.volumes.%s: no storage %q in fleet.yaml spec.storage", key, m.Storage)
		}
		if m.Path != "" && (!sharePathRe.MatchString(m.Path) || strings.Contains(m.Path, "..")) {
			return fmt.Errorf("spec.volumes.%s: path %q is not a folder in the share", key, m.Path)
		}
	}
	return nil
}

// StorageVolume is the compose definition of a stack volume on network
// storage. passwordVar is the env variable that must hold the password ("" for
// none), so it is never written into the compose file.
func StorageVolume(stack, key string, st Storage, m VolumeMount, passwordVar string) map[string]any {
	device := ""
	opts := []string{"addr=" + strings.Trim(st.Address, "[]")}
	sub := strings.Trim(path.Clean("/"+m.Path), "/")
	switch st.Type {
	case "cifs":
		device = "//" + st.Address + "/" + strings.Trim(st.Share, "/")
		if sub != "" {
			device += "/" + sub
		}
		if st.Username != "" {
			opts = append(opts, "username="+st.Username)
		}
		if passwordVar != "" {
			opts = append(opts, "password=${"+passwordVar+"}")
		}
	case "nfs":
		device = ":" + path.Join(st.Share, sub)
	}
	opts = append(opts, st.Options...)
	return map[string]any{
		// Its own name: a local volume with this key may already exist,
		// and Docker never changes an existing volume's options.
		"name":   stack + "_" + key + "_" + m.Storage,
		"driver": "local",
		"driver_opts": map[string]string{
			"type":   st.Type,
			"device": device,
			"o":      strings.Join(opts, ","),
		},
	}
}

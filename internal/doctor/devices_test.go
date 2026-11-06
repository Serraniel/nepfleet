// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestHostHardware(t *testing.T) {
	root := t.TempDir()
	mk := func(p, content string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("dev/serial/by-id/usb-Nabu_Casa_SkyConnect_v1.0-if00-port0", "")
	mk("dev/dri/renderD128", "")
	mk("sys/class/drm/renderD128/device/vendor", "0x1002\n")
	mk("dev/nvidia0", "")
	hw := HostHardware(root)
	if !slices.Equal(hw.Serial, []string{"/dev/serial/by-id/usb-Nabu_Casa_SkyConnect_v1.0-if00-port0"}) || !slices.Equal(hw.GPUs, []string{"amd", "nvidia"}) {
		t.Errorf("hardware = %+v", hw)
	}
	if hw := HostHardware(t.TempDir()); len(hw.Serial)+len(hw.GPUs) != 0 {
		t.Errorf("empty host: %+v", hw)
	}
}

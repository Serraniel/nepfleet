// SPDX-License-Identifier: AGPL-3.0-or-later

package render

import (
	"strings"
	"testing"

	"github.com/Serraniel/nepfleet/internal/template"
)

func TestWithHardware(t *testing.T) {
	compose := []byte("services:\n  ha:\n    image: ha\n  jf:\n    image: jf\n    devices: [\"/dev/dri:/dev/dri\"]\n  llm:\n    image: ollama\n")
	hw := []template.Hardware{
		{Input: "ZIGBEE", Services: []string{"ha"}, Target: "/dev/ttyUSB0"},
		{Input: "GPU", Services: []string{"jf"}},
		{Input: "LLM_GPU", Services: []string{"llm"}},
		{Input: "UNSET", Services: []string{"ha"}},
	}
	env := map[string]string{"ZIGBEE": "/dev/serial/by-id/usb-SkyConnect", "GPU": "intel", "LLM_GPU": "nvidia"}
	out, err := WithHardware(compose, hw, env, "compose", "")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"/dev/serial/by-id/usb-SkyConnect:/dev/ttyUSB0", "driver: nvidia", "capabilities:"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Count(s, "/dev/dri:/dev/dri") != 1 {
		t.Errorf("/dev/dri added twice:\n%s", s)
	}
	if _, err := WithHardware(compose, []template.Hardware{{Input: "GPU", Services: []string{"nope"}}}, env, "compose", ""); err == nil {
		t.Error("an unknown service must be an error")
	}
	if same, _ := WithHardware(compose, hw, map[string]string{"GPU": "none"}, "compose", ""); string(same) != string(compose) {
		t.Error("no hardware chosen must leave the file unchanged")
	}
}

func TestWithHardwareEdges(t *testing.T) {
	merged := []byte("x-base: &base\n  image: ha\n  devices: [\"/dev/ttyACM0:/dev/ttyACM0\"]\nservices:\n  ha:\n    <<: *base\n  jf:\n    image: jf\n  alias: *base\n")
	env := map[string]string{"Z": "/dev/serial/by-id/usb-x", "GPU": "intel"}
	out, err := WithHardware(merged, []template.Hardware{{Input: "Z", Services: []string{"ha"}}, {Input: "GPU", Services: []string{"jf"}}}, env, "compose", "989")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "/dev/ttyACM0:/dev/ttyACM0") || !strings.Contains(s, "/dev/serial/by-id/usb-x:/dev/serial/by-id/usb-x") {
		t.Errorf("inherited devices must be kept:\n%s", s)
	}
	if !strings.Contains(s, "group_add:") || !strings.Contains(s, "989") {
		t.Errorf("render group missing:\n%s", s)
	}
	if _, err := WithHardware(merged, []template.Hardware{{Input: "Z", Services: []string{"alias"}}}, env, "compose", ""); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Errorf("alias service: %v", err)
	}
	if _, err := WithHardware(merged, []template.Hardware{{Input: "Z", Services: []string{"ha"}}}, env, "swarm", ""); err == nil {
		t.Error("swarm must refuse hardware")
	}
}

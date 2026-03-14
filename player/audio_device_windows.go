//go:build windows

package player

import (
	"fmt"
	"os/exec"
	"strings"
)

// ListAudioDevices lists audio output devices via PowerShell on Windows.
func ListAudioDevices() ([]AudioDevice, error) {
	// Use Get-CimInstance Win32_SoundDevice for basic sound card enumeration.
	script := `Get-CimInstance Win32_SoundDevice | ForEach-Object { $_.Name + '|' + $_.DeviceID }`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", script).Output()
	if err != nil {
		return nil, fmt.Errorf("powershell: %w", err)
	}

	var devices []AudioDevice
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, id, _ := strings.Cut(line, "|")
		devices = append(devices, AudioDevice{
			Index:       i,
			Name:        strings.TrimSpace(id),
			Description: strings.TrimSpace(name),
			Active:      i == 0, // first device is typically the default
		})
	}
	return devices, nil
}

// PrepareAudioDevice is a no-op on Windows — the system default output
// device is used. Returns a no-op cleanup.
func PrepareAudioDevice(device string) func() {
	return func() {}
}

// SwitchAudioDevice is not supported on Windows at runtime.
func SwitchAudioDevice(deviceName string) error {
	return fmt.Errorf("runtime audio device switching is not supported on Windows; set the default device in Windows Settings")
}

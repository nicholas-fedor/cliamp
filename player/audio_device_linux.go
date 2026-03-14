//go:build linux

package player

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ListAudioDevices returns available output sinks via pactl.
// Works on PulseAudio and PipeWire (via pipewire-pulse).
func ListAudioDevices() ([]AudioDevice, error) {
	defaultSink := ""
	if out, err := exec.Command("pactl", "get-default-sink").Output(); err == nil {
		defaultSink = strings.TrimSpace(string(out))
	}

	out, err := exec.Command("pactl", "list", "sinks").Output()
	if err != nil {
		return nil, fmt.Errorf("pactl: %w (is PulseAudio/PipeWire running?)", err)
	}

	var devices []AudioDevice
	var cur *AudioDevice

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Sink #") {
			idx, _ := strconv.Atoi(strings.TrimPrefix(line, "Sink #"))
			devices = append(devices, AudioDevice{Index: idx})
			cur = &devices[len(devices)-1]
		} else if cur != nil {
			switch {
			case strings.HasPrefix(line, "Name: "):
				cur.Name = strings.TrimPrefix(line, "Name: ")
				cur.Active = cur.Name == defaultSink
			case strings.HasPrefix(line, "Description: "):
				cur.Description = strings.TrimPrefix(line, "Description: ")
			}
		}
	}

	return devices, nil
}

// PrepareAudioDevice sets PIPEWIRE_NODE so the PipeWire ALSA plugin
// routes this process's audio to the named device.
// Must be called before player.New(). Returns a no-op cleanup.
func PrepareAudioDevice(device string) func() {
	os.Setenv("PIPEWIRE_NODE", device)
	return func() {}
}

// SwitchAudioDevice moves this process's active audio stream to a
// different output at runtime via pactl move-sink-input.
func SwitchAudioDevice(deviceName string) error {
	pid := os.Getpid()

	out, err := exec.Command("pactl", "list", "sink-inputs").Output()
	if err != nil {
		return fmt.Errorf("pactl: %w", err)
	}

	sinkInputIdx := -1
	currentIdx := 0
	inEntry := false

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Sink Input #") {
			idx, _ := strconv.Atoi(strings.TrimPrefix(line, "Sink Input #"))
			currentIdx = idx
			inEntry = true
		}
		if inEntry && strings.Contains(line, "application.process.id") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				pidStr := strings.Trim(strings.TrimSpace(parts[1]), `"`)
				if pidStr == strconv.Itoa(pid) {
					sinkInputIdx = currentIdx
					break
				}
			}
		}
	}

	if sinkInputIdx < 0 {
		return fmt.Errorf("no active audio stream found for PID %d", pid)
	}

	cmd := exec.Command("pactl", "move-sink-input",
		strconv.Itoa(sinkInputIdx), deviceName)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("move-sink-input: %s (%w)",
			strings.TrimSpace(string(out)), err)
	}

	return nil
}

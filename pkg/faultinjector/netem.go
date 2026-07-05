package faultinjector

import (
	"fmt"
	"time"
)

// Command carries command state for this package call path.
type Command struct {
	Name string
	Args []string
}

// NetemDelay carries netem delay state for this package call path.
type NetemDelay struct {
	Container string
	Device    string
	Delay     time.Duration
	Jitter    time.Duration
}

// Command returns command data for NetemDelay callers without handing out mutable receiver state.
func (f NetemDelay) Command() Command {
	device := firstNonEmpty(f.Device, "eth0")
	args := []string{"exec", f.Container, "tc", "qdisc", "add", "dev", device, "root", "netem", "delay", durationMillis(f.Delay)}
	if f.Jitter > 0 {
		args = append(args, durationMillis(f.Jitter))
	}
	return Command{Name: "docker", Args: args}
}

// NetemReset carries netem reset state for this package call path.
type NetemReset struct {
	Container string
	Device    string
}

// NetemLoss carries netem loss state for this package call path.
type NetemLoss struct {
	Container   string
	Device      string
	LossPercent float64
}

// Command returns command data for NetemLoss callers without handing out mutable receiver state.
func (f NetemLoss) Command() Command {
	return Command{
		Name: "docker",
		Args: []string{"exec", f.Container, "tc", "qdisc", "add", "dev", firstNonEmpty(f.Device, "eth0"), "root", "netem", "loss", fmt.Sprintf("%.2f%%", f.LossPercent)},
	}
}

// CPUThrottle carries cpu throttle state for this package call path.
type CPUThrottle struct {
	Container string
	CPUs      float64
}

// Command returns command data for CPUThrottle callers without handing out mutable receiver state.
func (f CPUThrottle) Command() Command {
	return Command{
		Name: "docker",
		Args: []string{"update", "--cpus", fmt.Sprintf("%.2f", f.CPUs), f.Container},
	}
}

// Command returns command data for NetemReset callers without handing out mutable receiver state.
func (f NetemReset) Command() Command {
	return Command{
		Name: "docker",
		Args: []string{"exec", f.Container, "tc", "qdisc", "del", "dev", firstNonEmpty(f.Device, "eth0"), "root"},
	}
}

// durationMillis keeps duration millis rules consistent for this package call path.
func durationMillis(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

// firstNonEmpty preserves explicit values while providing legacy fallbacks.
func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

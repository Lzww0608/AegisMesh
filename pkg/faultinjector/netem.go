package faultinjector

import (
	"fmt"
	"time"
)

type Command struct {
	Name string
	Args []string
}

type NetemDelay struct {
	Container string
	Device    string
	Delay     time.Duration
	Jitter    time.Duration
}

func (f NetemDelay) Command() Command {
	device := firstNonEmpty(f.Device, "eth0")
	args := []string{"exec", f.Container, "tc", "qdisc", "add", "dev", device, "root", "netem", "delay", durationMillis(f.Delay)}
	if f.Jitter > 0 {
		args = append(args, durationMillis(f.Jitter))
	}
	return Command{Name: "docker", Args: args}
}

type NetemReset struct {
	Container string
	Device    string
}

type NetemLoss struct {
	Container   string
	Device      string
	LossPercent float64
}

func (f NetemLoss) Command() Command {
	return Command{
		Name: "docker",
		Args: []string{"exec", f.Container, "tc", "qdisc", "add", "dev", firstNonEmpty(f.Device, "eth0"), "root", "netem", "loss", fmt.Sprintf("%.2f%%", f.LossPercent)},
	}
}

type CPUThrottle struct {
	Container string
	CPUs      float64
}

func (f CPUThrottle) Command() Command {
	return Command{
		Name: "docker",
		Args: []string{"update", "--cpus", fmt.Sprintf("%.2f", f.CPUs), f.Container},
	}
}

func (f NetemReset) Command() Command {
	return Command{
		Name: "docker",
		Args: []string{"exec", f.Container, "tc", "qdisc", "del", "dev", firstNonEmpty(f.Device, "eth0"), "root"},
	}
}

func durationMillis(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

package faultinjector

import (
	"reflect"
	"testing"
	"time"
)

func TestNetemDelayCommandBuildsDockerTcArgs(t *testing.T) {
	fault := NetemDelay{
		Container: "user-v2",
		Device:    "eth0",
		Delay:     200 * time.Millisecond,
		Jitter:    50 * time.Millisecond,
	}

	got := fault.Command()
	want := Command{
		Name: "docker",
		Args: []string{"exec", "user-v2", "tc", "qdisc", "add", "dev", "eth0", "root", "netem", "delay", "200ms", "50ms"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestNetemResetCommandBuildsDockerTcDeleteArgs(t *testing.T) {
	got := NetemReset{Container: "user-v2", Device: "eth0"}.Command()
	want := Command{
		Name: "docker",
		Args: []string{"exec", "user-v2", "tc", "qdisc", "del", "dev", "eth0", "root"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestNetemLossCommandBuildsDockerTcArgs(t *testing.T) {
	got := NetemLoss{
		Container:   "user-v2",
		Device:      "eth0",
		LossPercent: 2.5,
	}.Command()
	want := Command{
		Name: "docker",
		Args: []string{"exec", "user-v2", "tc", "qdisc", "add", "dev", "eth0", "root", "netem", "loss", "2.50%"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestCPUThrottleCommandBuildsDockerUpdateArgs(t *testing.T) {
	got := CPUThrottle{Container: "user-v2", CPUs: 0.25}.Command()
	want := Command{
		Name: "docker",
		Args: []string{"update", "--cpus", "0.25", "user-v2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected command:\n got: %+v\nwant: %+v", got, want)
	}
}

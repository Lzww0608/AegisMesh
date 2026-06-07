package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aegismesh/aegismesh/pkg/faultinjector"
)

func main() {
	kind := flag.String("kind", "delay", "fault kind: delay, loss, cpu, reset")
	container := flag.String("container", "", "target Docker container")
	device := flag.String("device", "eth0", "network device inside the container")
	delay := flag.Duration("delay", 200*time.Millisecond, "netem delay duration")
	jitter := flag.Duration("jitter", 0, "netem delay jitter")
	lossPercent := flag.Float64("loss-percent", 1, "netem loss percentage")
	cpus := flag.Float64("cpus", 0.25, "docker CPU quota for cpu fault")
	execute := flag.Bool("execute", false, "execute the command instead of printing it")
	flag.Parse()

	if *container == "" {
		log.Fatal("--container is required")
	}

	command := buildCommand(*kind, *container, *device, *delay, *jitter, *lossPercent, *cpus)
	if !*execute {
		fmt.Println(shellLine(command))
		return
	}

	cmd := exec.Command(command.Name, command.Args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("execute fault command: %v", err)
	}
}

func buildCommand(kind string, container string, device string, delay time.Duration, jitter time.Duration, lossPercent float64, cpus float64) faultinjector.Command {
	switch strings.ToLower(kind) {
	case "delay":
		return faultinjector.NetemDelay{Container: container, Device: device, Delay: delay, Jitter: jitter}.Command()
	case "loss":
		return faultinjector.NetemLoss{Container: container, Device: device, LossPercent: lossPercent}.Command()
	case "cpu":
		return faultinjector.CPUThrottle{Container: container, CPUs: cpus}.Command()
	case "reset":
		return faultinjector.NetemReset{Container: container, Device: device}.Command()
	default:
		log.Fatalf("unsupported fault kind %q", kind)
		return faultinjector.Command{}
	}
}

func shellLine(command faultinjector.Command) string {
	parts := append([]string{command.Name}, command.Args...)
	return strings.Join(parts, " ")
}

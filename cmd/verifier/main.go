package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/aegismesh/aegismesh/pkg/verifier"
)

// main wires the command-line entry point and reports fatal setup or runtime errors.
func main() {
	specPath := flag.String("spec", "", "verifier YAML spec path")
	tracesPath := flag.String("traces", "", "trace JSONL path")
	flag.Parse()

	if *specPath == "" || *tracesPath == "" {
		log.Fatal("--spec and --traces are required")
	}

	specRaw, err := os.ReadFile(*specPath)
	if err != nil {
		log.Fatalf("read spec: %v", err)
	}
	spec, err := verifier.ParseSpec(specRaw)
	if err != nil {
		log.Fatalf("parse spec: %v", err)
	}

	tracesFile, err := os.Open(*tracesPath)
	if err != nil {
		log.Fatalf("open traces: %v", err)
	}
	defer tracesFile.Close()

	traces, err := verifier.LoadTraceJSONL(tracesFile)
	if err != nil {
		log.Fatalf("load traces: %v", err)
	}

	report := verifier.Verify(spec, traces)
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatalf("encode report: %v", err)
	}
	fmt.Println(string(out))
	if !report.Passed {
		os.Exit(1)
	}
}

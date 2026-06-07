package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/aegismesh/aegismesh/pkg/deathstarbench"
)

func main() {
	configPath := flag.String("config", "experiments/deathstarbench/social-network.yaml", "DeathStarBench integration config")
	flag.Parse()

	raw, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("read config: %v", err)
	}
	cfg, err := deathstarbench.ParseConfig(raw)
	if err != nil {
		log.Fatalf("parse config: %v", err)
	}
	plan := cfg.Plan()
	out, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		log.Fatalf("encode plan: %v", err)
	}
	fmt.Println(string(out))
}

package deathstarbench

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Benchmark   string                    `yaml:"benchmark" json:"benchmark"`
	Repo        string                    `yaml:"repo" json:"repo"`
	ComposeFile string                    `yaml:"compose_file" json:"compose_file"`
	Controller  string                    `yaml:"controller" json:"controller"`
	Frontend    FrontendConfig            `yaml:"frontend" json:"frontend"`
	Services    map[string]ServiceMapping `yaml:"services" json:"services"`
}

type FrontendConfig struct {
	URL      string `yaml:"url" json:"url"`
	Workload string `yaml:"workload" json:"workload"`
}

type ServiceMapping struct {
	AegisName string `yaml:"aegis_name" json:"aegis_name"`
	Port      int    `yaml:"port" json:"port"`
}

type IntegrationPlan struct {
	ComposeCommand  string            `json:"compose_command"`
	WorkloadCommand string            `json:"workload_command"`
	Environment     map[string]string `json:"environment"`
	ServiceNames    []string          `json:"service_names"`
}

func ParseConfig(raw []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Services == nil {
		cfg.Services = map[string]ServiceMapping{}
	}
	return cfg, nil
}

func (c Config) Plan() IntegrationPlan {
	serviceNames := make([]string, 0, len(c.Services))
	mappingParts := make([]string, 0, len(c.Services))
	for service, mapping := range c.Services {
		serviceNames = append(serviceNames, service)
		mappingParts = append(mappingParts, fmt.Sprintf("%s=%s:%d", service, mapping.AegisName, mapping.Port))
	}
	sort.Strings(serviceNames)
	sort.Strings(mappingParts)

	workload := c.Frontend.Workload
	if workload == "" {
		workload = "wrk2"
	}
	frontendURL := c.Frontend.URL
	if frontendURL == "" {
		frontendURL = "http://localhost:8080"
	}

	return IntegrationPlan{
		ComposeCommand:  fmt.Sprintf("docker compose -f %s up -d", c.ComposeFile),
		WorkloadCommand: fmt.Sprintf("%s -t4 -c64 -d60s %s", workload, frontendURL),
		Environment: map[string]string{
			"AEGIS_CONTROLLER":  c.Controller,
			"AEGIS_SERVICE_MAP": strings.Join(mappingParts, ","),
		},
		ServiceNames: serviceNames,
	}
}

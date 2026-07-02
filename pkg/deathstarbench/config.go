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
	ReadyURL string `yaml:"ready_url" json:"ready_url,omitempty"`
	Workload string `yaml:"workload" json:"workload"`
	Command  string `yaml:"command" json:"command,omitempty"`
}

type ServiceMapping struct {
	AegisName string `yaml:"aegis_name" json:"aegis_name"`
	Port      int    `yaml:"port" json:"port"`
}

type IntegrationPlan struct {
	ComposeCommand     string            `json:"compose_command"`
	ComposeDownCommand string            `json:"compose_down_command"`
	WorkloadCommand    string            `json:"workload_command"`
	FrontendURL        string            `json:"frontend_url"`
	ReadyURL           string            `json:"ready_url"`
	Environment        map[string]string `json:"environment"`
	ServiceNames       []string          `json:"service_names"`
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

	frontendURL := frontendURL(c)
	readyURL := readyURL(c)
	workloadCommand := c.Frontend.Command
	if workloadCommand == "" {
		workload := c.Frontend.Workload
		if workload == "" {
			workload = "wrk2"
		}
		workloadCommand = fmt.Sprintf("%s -t4 -c64 -d60s %s", workload, frontendURL)
	}

	return IntegrationPlan{
		ComposeCommand:     fmt.Sprintf("docker compose -f %s up -d", shellQuote(c.ComposeFile)),
		ComposeDownCommand: fmt.Sprintf("docker compose -f %s down --remove-orphans", shellQuote(c.ComposeFile)),
		WorkloadCommand:    workloadCommand,
		FrontendURL:        frontendURL,
		ReadyURL:           readyURL,
		Environment: map[string]string{
			"AEGIS_CONTROLLER":  c.Controller,
			"AEGIS_SERVICE_MAP": strings.Join(mappingParts, ","),
		},
		ServiceNames: serviceNames,
	}
}

func readyURL(c Config) string {
	if c.Frontend.ReadyURL != "" {
		return c.Frontend.ReadyURL
	}
	return frontendURL(c)
}

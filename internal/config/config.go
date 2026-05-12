package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	Entry       string `json:"entry"`
	Out         string `json:"out"`
	Package     string `json:"package"`
	ModuleMode  string `json:"moduleMode"`
	JSONTags    bool   `json:"jsonTags"`
	NumberMode  string `json:"numberMode"`
	Strict      bool   `json:"strict"`
	EmitOnError bool   `json:"emitOnError"`
	Framework   string `json:"framework"`
}

func Default() Config {
	return Config{
		Entry:      "./src",
		Out:        "./gen",
		Package:    "api",
		ModuleMode: "single-package",
		JSONTags:   true,
		NumberMode: "float64",
		Strict:     true,
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) WithPackage(name string) Config {
	if name != "" {
		c.Package = name
	}
	return c
}

func (c Config) WithOut(out string) Config {
	if out != "" {
		c.Out = out
	}
	return c
}

func (c Config) WithFramework(framework string) Config {
	if framework != "" {
		c.Framework = framework
	}
	return c
}

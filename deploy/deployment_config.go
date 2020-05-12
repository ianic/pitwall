package deploy

import (
	"fmt"
	"io/ioutil"

	"github.com/manifoldco/promptui"
	"github.com/minus5/svckit/log"
	yaml "gopkg.in/yaml.v2"
)

// DeploymentConfig containes parameters for specific deployment
type DeploymentConfig struct {
	root         string
	deployment   string
	FederatedDcs string `yaml:"federated_dcs"`
	Datacenters  map[string]*DcConfig
}

// DcConfig contains parameters for specific datacenter
type DcConfig struct {
	Services map[string]*ServiceConfig `yaml:"services,omitempty"`
}

// NewDeploymentConfig creates new config for specific deployment
func NewDeploymentConfig(root, deployment string) (*DeploymentConfig, error) {
	c := &DeploymentConfig{
		root:       root,
		deployment: deployment,
	}
	return c, c.load()
}

func (c *DeploymentConfig) serviceNames() []string {
	var names []string
	for _, s := range c.Datacenters {
		for k := range s.Services {
			names = append(names, k)
		}
	}
	return names
}

// Select service
func (c *DeploymentConfig) Select() (string, error) {
	names := c.serviceNames()
	prompt := promptui.Select{
		Label: "Select service",
		Items: names,
		Size:  10,
		Templates: &promptui.SelectTemplates{
			Selected: string([]byte("\033[" + "1A")),
		},
	}
	idx, _, err := prompt.Run()
	return names[idx], err
}

// Find returns config for specific service
func (c *DeploymentConfig) Find(service string) *ServiceConfig {
	for _, s := range c.Datacenters {
		for k, sc := range s.Services {
			if k == service {
				return sc
			}
		}
	}
	return nil
}

// FindForDc returns config for specific service and datacenter
func (c *DeploymentConfig) FindForDc(service, dc string) *ServiceConfig {
	for d, s := range c.Datacenters {
		for k, sc := range s.Services {
			if d == dc && k == service {
				return sc
			}
		}
	}
	return nil
}

// FindDatacenters finds datacenters for service if it exists
func (c *DeploymentConfig) FindDatacenters(service string) []string {
	dcs := []string{}
	for d, s := range c.Datacenters {
		for k := range s.Services {
			if k == service {
				dcs = append(dcs, d)
				break
			}
		}
	}
	return dcs
}

// FileName returns config.yml for dc
func (c *DeploymentConfig) FileName() string {
	return fmt.Sprintf("%s/deployments/%s/config.yml", c.root, c.deployment)
}

func (c *DeploymentConfig) load() error {
	fn := c.FileName()
	data, err := ioutil.ReadFile(fn)
	if err != nil {
		log.Error(err)
		return err
	}
	if err := yaml.Unmarshal([]byte(data), c); err != nil {
		log.Error(err)
		return err
	}
	log.S("from", fn).Debug("deployment config")
	return nil
}

// ServiceConfig represent structure for config.yml
type ServiceConfig struct {
	Image       string
	Count       int                    `yaml:"count,omitempty"`
	HostGroup   string                 `yaml:"hostgroup,omitempty"`
	Node        string                 `yaml:"node,omitempty"`
	CPU         int                    `yaml:"cpu,omitempty"`
	Memory      int                    `yaml:"mem,omitempty"`
	Environment map[string]string      `yaml:"env,omitempty"`
	Arguments   []string               `yaml:"arg,omitempty"`
	Volumes     []string               `yaml:"vol,omitempty"`
	Constraints map[string]*Constraint `yaml:"constraints,omitempty"`
}

type Constraint struct {
	Attribute string `yaml:"attribute,omitempty"`
	Operator  string `yaml:"operator,omitempty"`
	Value     string `yaml:"value,omitempty"`
}

// Save changes to config.yml
func (c *DeploymentConfig) Save() error {
	fn := c.FileName()
	buf, err := yaml.Marshal(c)
	if err != nil {
		log.S("fn", fn).Error(err)
		return err
	}
	return ioutil.WriteFile(fn, buf, 0644)
}

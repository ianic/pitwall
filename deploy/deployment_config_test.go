package deploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadConfig(t *testing.T) {
	// load invalid configuration
	cfg, err := NewDeploymentConfig("./fixture", "test2")
	assert.Error(t, err)
	assert.NotNil(t, cfg)
	assert.Len(t, cfg.Datacenters, 0) // number of datacenters

	// load valid configuration
	cfg, err = NewDeploymentConfig("./fixture", "test")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, "datacenter1 datacenter2", cfg.FederatedDcs) // fedrated dscs
	assert.Len(t, cfg.Datacenters, 3)                            // number of datacenters

	// find one service
	svc := cfg.Find("service_test1")
	assert.NotNil(t, svc)

	// find one service in datacenter
	svc = cfg.FindForDc("service_test1", "datacenter1")
	assert.NotNil(t, svc)

	// find non existing service in datacenter
	svc = cfg.FindForDc("service_test1", "datacenter2")
	assert.Nil(t, svc)

	// all avaliable service names
	allsvc := cfg.serviceNames()
	assert.Len(t, allsvc, 3)

	// all datacenters for service
	alldc := cfg.FindDatacenters("service_test2")
	assert.Len(t, alldc, 2)
}

func TestLoadServiceParams(t *testing.T) {
	// load valid configuration
	cfg, err := NewDeploymentConfig("./fixture", "test")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// find one service in datacenter
	svc := cfg.FindForDc("service_test1", "datacenter1")
	assert.NotNil(t, svc)

	// check values
	assert.Equal(t, "service_test1_image", svc.Image)
	assert.Equal(t, 1, svc.Count)
	assert.Equal(t, "app", svc.HostGroup)
	assert.Equal(t, "app1", svc.Node)
	assert.Equal(t, 64, svc.CPU)
	assert.Equal(t, 128, svc.Memory)

	// check environmet
	assert.Len(t, svc.Environment, 5)
	for k, v := range svc.Environment {
		assert.Equal(t, k+"_set", v)
	}

	// check arguments
	assert.Len(t, svc.Arguments, 4)
	assert.Equal(t, "-argument", svc.Arguments[0])
	assert.Equal(t, "argument_set", svc.Arguments[1])
	assert.Equal(t, "-argument_var1", svc.Arguments[2])
	assert.Equal(t, "argument_var1_set", svc.Arguments[3])

	// check volumes
	assert.Len(t, svc.Volumes, 2)
	assert.Equal(t, "name-of-the-volume1:/path/in/container1", svc.Volumes[0])
	assert.Equal(t, "name-of-the-volume2:/path/in/container2", svc.Volumes[1])
}

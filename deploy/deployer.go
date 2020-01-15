package deploy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/jobspec"
	"github.com/minus5/svckit/log"
)

// copying from https://github.com/hashicorp/nomad/blob/74c270d89a193ac6695e1116d4e25c681322cc98/nomad/structs/structs.go
// i had a problem with including github.com/hashicorp/nomad/nomad/structs
const (
	JobTypeService             = "service"
	DeploymentStatusRunning    = "running"
	DeploymentStatusSuccessful = "successful"

	// FederatedDcsEnv is name of the environment variable containing datacenter names
	FederatedDcsEnv = "SVCKIT_FEDERATED_DCS"
	// DeploymentEnv is name of the environment variable containing deployment name
	DeploymentEnv = "deployment"
)

//Deployer has all deployment related objects
type Deployer struct {
	root            string
	service         string
	image           string
	address         string
	config          *DeploymentConfig
	job             *api.Job
	cli             *api.Client
	jobModifyIndex  uint64
	jobEvalID       string
	jobDeploymentID string
	region          string
	dc              string
	cdc             string // datacenter set in config file for service
	deployment      string
}

// NewDeployer is used to create new deployer
func NewDeployer(root, service, image string, config *DeploymentConfig, address, cdc, deployment string) *Deployer {
	return &Deployer{
		root:       root,
		service:    service,
		image:      image,
		config:     config,
		address:    address,
		cdc:        cdc,
		deployment: deployment,
	}
}

// Go function executes all needed steps for a new deployment
// loadServiceConfig - loads Nomad job configuration from file *.nomad
// connect - connects to a Nomad server (from Consul)
// validate - job check is it syntactically correct
// plan - dry-run a job update to determine its effects
// register - register a job to scheduler
// status - status of the submited job
func (d *Deployer) Go(dryRun bool) error {
	steps := []func() error{
		d.loadServiceConfig,
		d.connect,
		d.validate,
	}
	if dryRun {
		steps = append(steps, d.show)
	} else {
		steps = append(steps,
			[]func() error{
				d.plan,
				d.register,
				d.status,
			}...)
	}
	return runSteps(steps)
}

func (d *Deployer) show() error {
	log.Info("show")
	buf, _ := json.MarshalIndent(d.job, "  ", "  ")
	fmt.Printf("%s\n", buf)
	return nil
}

// checkServiceConfig - does config.yml exists in dc directory
func (d *Deployer) checkServiceConfig() error {
	if s := d.config.FindForDc(d.service, d.cdc); s == nil {
		return fmt.Errorf("service %s not found in datacenter config", d.service)
	}
	return nil
}

// plan envoke the scheduler in a dry-run mode with new jobs or when updating existing jobs to determine what would happen if the job is submitted
func (d *Deployer) plan() error {
	jp, _, err := d.cli.Jobs().Plan(d.job, false, nil)
	if err != nil {
		return err
	}
	d.jobModifyIndex = jp.JobModifyIndex
	log.I("modifyIndex", int(jp.JobModifyIndex)).Info("job planned")
	return nil
}

// register a job
// If EnforceRegister is set then the job will only be registered if the passed
// JobModifyIndex matches the current Jobs index. If the index is zero, the
// register only occurs if the job is new
func (d *Deployer) register() error {
	jr, _, err := d.cli.Jobs().EnforceRegister(d.job, d.jobModifyIndex, nil)
	if err != nil {
		return err
	}
	// EvalID is the eval ID of the plan being applied. The modify index of the
	// evaluation is updated as part of applying the plan to ensure that subsequent
	// scheduling events for the same job will wait for the index that last produced
	// state changes. This is necessary for blocked evaluations since they can be
	// processed many times, potentially making state updates, without the state of
	// the evaluation itself being updated.
	d.jobEvalID = jr.EvalID
	if err := d.getDeploymentID(); err != nil {
		return err
	}
	log.S("evalID", jr.EvalID).S("deploymentID", d.jobDeploymentID).Info("job registered")
	return nil
}

// DeploymentID is the ID of the deployment to update
func (d *Deployer) getDeploymentID() error {
	for {
		ev, _, err := d.cli.Evaluations().Info(d.jobEvalID, nil)
		if err != nil {
			return err
		}
		if ev.DeploymentID != "" {
			d.jobDeploymentID = ev.DeploymentID
			return nil
		}
		if ev.Status == "complete" && ev.Type != JobTypeService {
			return nil
		}
		time.Sleep(time.Second)
	}
}

// status of the submited job
func (d *Deployer) status() error {
	depID := d.jobDeploymentID
	if depID == "" {
		return nil
	}

	var canaryChan chan interface{}
	deploymentChan := make(chan interface{})

	if d.job.Update != nil && d.job.Update.Canary != nil && *d.job.Update.Canary != 0 {
		canaryChan = make(chan interface{})
		go d.canaryPromote(depID, canaryChan, deploymentChan)
	}

	t := time.Now()
	q := &api.QueryOptions{WaitIndex: 1, AllowStale: true, WaitTime: time.Duration(5 * time.Second)}

	// signal canaryPromote goroutine to exit if it's still runing on return
	defer func() {
		if canaryChan != nil {
			close(canaryChan)
		}
	}()

	for {
		dep, meta, err := d.cli.Deployments().Info(depID, q)

		if err != nil {
			return err
		}

		select {
		case <-deploymentChan:
			// if promotion didn't succeed, and deployment is still running, fail it
			if dep.Status == DeploymentStatusRunning {
				log.Info("failing deployment")
				_, _, err := d.cli.Deployments().Fail(depID, nil)
				if err != nil {
					return fmt.Errorf("error while manually failing deployment: %v", err)
				}
			}

			return fmt.Errorf("deployment failed")
		default:
			break

		}

		q.WaitIndex = meta.LastIndex
		du := fmt.Sprintf("%.2fs", time.Since(t).Seconds())
		if dep.Status == DeploymentStatusRunning {
			for _, v := range dep.TaskGroups {
				log.S("running", du).
					//S("group", k).
					I("desired", v.DesiredTotal).
					I("placed", v.PlacedAllocs).
					I("healthy", v.HealthyAllocs).
					Debug("checking status")
			}
			continue
		}
		if dep.Status == DeploymentStatusSuccessful {
			log.S("after", du).Info("deployment successful")
			break
		}

		d.checkFailedDeployment(depID)

		return fmt.Errorf("deployment failed status: %s %s",
			dep.Status,
			dep.StatusDescription)
	}
	return nil
}

// find and show deployment error
func (d *Deployer) checkFailedDeployment(depID string) {
	al, _, err := d.cli.Deployments().Allocations(depID, nil)
	if err == nil {
		for _, a := range al {
			for _, s := range a.TaskStates {
				for _, e := range s.Events {
					if e.DriverError != "" ||
						e.DownloadError != "" ||
						e.ValidationError != "" ||
						e.SetupError != "" ||
						e.VaultError != "" {
						fmt.Printf("%s%s%s%s%s",
							warn(e.DriverError),
							warn(e.DownloadError),
							warn(e.ValidationError),
							warn(e.SetupError),
							warn(e.VaultError))
					}
				}
			}
		}
	}
}

// promote canary allocations when all are healthy
func (d *Deployer) canaryPromote(depID string, shutdownChan, deploymentChan chan interface{}) {
	log.S("deploymentID", depID).Info("promoting deployment")

	autoPromote := time.Tick(5 * time.Second)

	for {

		select {
		case <-autoPromote:
			if healthy := d.checkCanaryHealth(depID); !healthy {
				continue
			}

			_, _, err := d.cli.Deployments().PromoteAll(depID, nil)
			if err != nil {
				log.Errorf("error while promoting: %v", err)
				close(deploymentChan)
			}
			return

		case <-shutdownChan:
			return
		}
	}

}

// check if all canary allocations are healthy
func (d *Deployer) checkCanaryHealth(depID string) bool {
	var unhealthy int

	dep, _, err := d.cli.Deployments().Info(depID, &api.QueryOptions{AllowStale: true})
	if err != nil {
		log.Errorf("unable to query deployment %s for health: %v", depID, err)
		return false
	}

	for _, taskInfo := range dep.TaskGroups {
		if taskInfo.DesiredCanaries == 0 {
			continue
		}

		if taskInfo.DesiredCanaries != taskInfo.HealthyAllocs {
			unhealthy++
		}
	}

	return unhealthy == 0

}

// loadServiceConfig from dc config.yml
func (d *Deployer) loadServiceConfig() error {
	fn := fmt.Sprintf("%s/nomad/service/%s.nomad", d.root, d.service)
	job, err := jobspec.ParseFile(fn)
	if err != nil {
		fn = fmt.Sprintf("%s/nomad/system/%s.nomad", d.root, d.service)
		job, err = jobspec.ParseFile(fn)
	}
	if err != nil {
		return err
	}

	log.S("from", fn).Debug("loaded config")
	d.job = job
	return d.checkServiceConfig()
}

// connect to Nomad server (from Consul)
func (d *Deployer) connect() error {
	c := &api.Config{}
	addr := d.address
	c = c.ClientConfig("", addr, false)
	cli, err := api.NewClient(c)
	if err != nil {
		return err
	}
	log.S("nomad", addr).Info("connected")
	d.cli = cli
	// server default dc and region
	dc, err := d.cli.Agent().Datacenter()
	if err != nil {
		return err
	}
	region, err := d.cli.Agent().Region()
	if err != nil {
		return err
	}
	d.dc = dc
	d.region = region
	return nil
}

// validate the job to check is it syntactically correct
// combines Nomad job file and config.yml for specific datacenter
func (d *Deployer) validate() error {

	d.job.Region = &d.region
	d.job.Datacenters = []string{}
	d.job.AddDatacenter(d.dc)

	s := d.config.FindForDc(d.service, d.cdc)
	if s.HostGroup != "" {
		d.job.Constrain(api.NewConstraint("${meta.hostgroup}", "=", s.HostGroup))
	}
	if s.Node != "" {
		d.job.Constrain(api.NewConstraint("${meta.node}", "=", s.Node))
	}

	for _, tg := range d.job.TaskGroups {
		if !(*tg.Name == d.service || *tg.Name == "services") {
			continue
		}
		if s.Count > 0 {
			tg.Count = &s.Count
			log.I("count", s.Count).Debug("setting")
		}

		for _, ta := range tg.Tasks {
			if !(ta.Name == d.service || ta.Name == "service") {
				continue
			}

			// set image
			ta.Config["image"] = d.image
			s.Image = d.image
			log.S("image", s.Image).Debug("setting")

			// set resources
			if s.CPU != 0 {
				ta.Resources.CPU = &s.CPU
				log.I("cpu", s.CPU).Debug("setting")
			}
			if s.Memory != 0 {
				ta.Resources.MemoryMB = &s.Memory
				log.I("memory", s.Memory).Debug("setting")
			}

			// replace arguments
			if len(s.Arguments) > 0 {
				log.I("args_len", len(s.Arguments)).Debug("setting")
				ta.Config["args"] = s.Arguments
			}

			// set environment variables
			if d.config.FederatedDcs != "" {
				ta.Env[FederatedDcsEnv] = d.config.FederatedDcs
			}
			if d.deployment != "" {
				ta.Env[DeploymentEnv] = d.deployment
			}
			for k, v := range s.Environment {
				if v != "" {
					ta.Env[k] = v
					log.S(k, v).Debug("setting env")
				}
			}

			// replace volumes
			if len(s.Volumes) > 0 {
				log.I("vol_len", len(s.Volumes)).Debug("setting")
				ta.Config["volumes"] = s.Volumes
			}
		}
	}

	_, _, err := d.cli.Jobs().Validate(d.job, nil)
	if err != nil {
		return err
	}
	log.Info("job validated")
	return nil
}

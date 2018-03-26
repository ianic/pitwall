package deploy

import (
	"fmt"
	"time"

	"github.com/hashicorp/nomad/api"
	"github.com/hashicorp/nomad/jobspec"
	nomadStructs "github.com/hashicorp/nomad/nomad/structs"
	"github.com/minus5/svckit/log"
)

//Deployer has all deployment related objects
type Deployer struct {
	root            string
	dc              string
	service         string
	image           string
	address         string
	config          *DcConfig
	job             *api.Job
	cli             *api.Client
	jobModifyIndex  uint64
	jobEvalID       string
	jobDeploymentID string
}

// NewDeployer is used to create new deployer
func NewDeployer(root, dc, service, image string, config *DcConfig, address string) *Deployer {
	return &Deployer{
		root:    root,
		dc:      dc,
		service: service,
		image:   image,
		config:  config,
		address: address,
	}
}

// Go function executes all needed steps for a new deployment
// loadServiceConfig - loads Nomad job configuration from file *.nomad
// connect - connects to a Nomad server (from Consul)
// validate - job check is it syntactically correct
// plan - dry-run a job update to determine its effects
// register - register a job to scheduler
// status - status of the submited job
func (d *Deployer) Go() error {
	steps := []func() error{
		d.loadServiceConfig,
		d.connect,
		d.validate,
		d.plan,
		d.register,
		d.status,
	}
	return runSteps(steps)
}

// checkServiceConfig - does config.yml exists in dc directory
func (d *Deployer) checkServiceConfig() error {
	if _, ok := d.config.Services[d.service]; !ok {
		return fmt.Errorf("service %d not found in datacenter config", d.service)
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
		if ev.Status == "complete" && ev.Type != nomadStructs.JobTypeService {
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
	t := time.Now()
	q := &api.QueryOptions{WaitIndex: 1, AllowStale: true, WaitTime: time.Duration(5 * time.Second)}
	for {
		dep, meta, err := d.cli.Deployments().Info(depID, q)
		if err != nil {
			return err
		}
		q.WaitIndex = meta.LastIndex
		du := fmt.Sprintf("%.2fs", time.Since(t).Seconds())
		if dep.Status == nomadStructs.DeploymentStatusRunning {
			log.S("running", du).Debug("checking status")
			continue
		}
		if dep.Status == nomadStructs.DeploymentStatusSuccessful {
			log.S("after", du).Info("deployment successful")
			break
		}

		// find and show error
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
		return fmt.Errorf("deployment failed status: %s %s",
			dep.Status,
			dep.StatusDescription)
	}
	return nil
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
	c = c.ClientConfig(d.config.Dc, addr, false)
	cli, err := api.NewClient(c)
	if err != nil {
		return err
	}
	log.S("nomad", addr).Info("connected")
	d.cli = cli
	return nil
}

// validate the job to check is it syntactically correct
// combines Nomad job file and config.yml for specific datacenter
func (d *Deployer) validate() error {
	d.job.Region = &d.config.Region
	d.job.AddDatacenter(d.config.Dc)

	s := d.config.Services[d.service]
	if s.DcRegion != "" {
		d.job.Constrain(api.NewConstraint("${meta.dc_region}", "=", s.DcRegion))
	}
	if s.HostGroup != "" {
		d.job.Constrain(api.NewConstraint("${meta.hostgroup}", "=", s.HostGroup))
	}
	if s.Node != "" {
		d.job.Constrain(api.NewConstraint("${meta.node}", "=", s.Node))
	}

	for _, tg := range d.job.TaskGroups {
		if *tg.Name == d.service {
			if s.Count > 0 {
				tg.Count = &s.Count
			}
			for _, ta := range tg.Tasks {
				if ta.Name == d.service {
					ta.Config["image"] = d.image
					s.Image = d.image
				}
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

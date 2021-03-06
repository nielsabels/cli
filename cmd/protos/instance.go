package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/pkg/errors"
	"github.com/protosio/cli/internal/cloud"
	"github.com/protosio/cli/internal/release"
	ssh "github.com/protosio/cli/internal/ssh"
	"github.com/urfave/cli/v2"
)

var cmdInstance *cli.Command = &cli.Command{
	Name:  "instance",
	Usage: "Manage Protos instances",
	Subcommands: []*cli.Command{
		{
			Name:  "ls",
			Usage: "List instances",
			Action: func(c *cli.Context) error {
				return listInstances()
			},
		},
		{
			Name:      "deploy",
			ArgsUsage: "<name>",
			Usage:     "Deploy a new Protos instance",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:        "cloud",
					Usage:       "Specify which `CLOUD` to deploy the instance on",
					Required:    true,
					Destination: &cloudName,
				},
				&cli.StringFlag{
					Name:        "location",
					Usage:       "Specify one of the supported `LOCATION`s to deploy the instance in (cloud specific)",
					Required:    true,
					Destination: &cloudLocation,
				},
				&cli.StringFlag{
					Name:        "version",
					Usage:       "Specify Protos `VERSION` to deploy",
					Required:    false,
					Destination: &protosVersion,
				},
			},
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				releases, err := getProtosReleases()
				if err != nil {
					return err
				}
				var release release.Release
				if protosVersion == "" {
					release, err = releases.GetLatest()
					if err != nil {
						return err
					}
				} else {
					release, err = releases.GetVersion(protosVersion)
					if err != nil {
						return err
					}
				}

				_, err = deployInstance(name, cloudName, cloudLocation, release)
				return err
			},
		},
		{
			Name:      "delete",
			ArgsUsage: "<name>",
			Usage:     "Delete instance",
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				return deleteInstance(name)
			},
		},
		{
			Name:      "start",
			ArgsUsage: "<name>",
			Usage:     "Power on instance",
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				return startInstance(name)
			},
		},
		{
			Name:      "stop",
			ArgsUsage: "<name>",
			Usage:     "Power off instance",
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				return stopInstance(name)
			},
		},
		{
			Name:      "tunnel",
			ArgsUsage: "<name>",
			Usage:     "Creates SSH encrypted tunnel to instance dashboard",
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				return tunnelInstance(name)
			},
		},
		{
			Name:      "key",
			ArgsUsage: "<name>",
			Usage:     "Prints to stdout the SSH key associated with the instance",
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				return keyInstance(name)
			},
		},
	},
}

//
// Instance methods
//

func listInstances() error {
	instances, err := dbp.GetAllInstances()
	if err != nil {
		return err
	}

	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 0, 0, 2, ' ', 0)

	defer w.Flush()

	fmt.Fprintf(w, " %s\t%s\t%s\t%s\t%s\t%s\t", "Name", "IP", "Cloud", "VM ID", "Location", "Status")
	fmt.Fprintf(w, "\n %s\t%s\t%s\t%s\t%s\t%s\t", "----", "--", "-----", "-----", "--------", "------")
	for _, instance := range instances {
		fmt.Fprintf(w, "\n %s\t%s\t%s\t%s\t%s\t%s\t", instance.Name, instance.PublicIP, instance.CloudName, instance.VMID, instance.Location, "n/a")
	}
	fmt.Fprint(w, "\n")
	return nil
}

func deployInstance(instanceName string, cloudName string, cloudLocation string, release release.Release) (cloud.InstanceInfo, error) {
	protosImage := "protos-" + release.Version

	// init cloud
	provider, err := dbp.GetCloud(cloudName)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrapf(err, "Could not retrieve cloud '%s'", cloudName)
	}
	client := provider.Client()
	err = client.Init(provider.Auth, cloudLocation)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrapf(err, "Failed to connect to cloud provider '%s'(%s) API", cloudName, provider.Type.String())
	}

	// add image
	imageID := ""
	images, err := client.GetImages()
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrap(err, "Failed to initialize Protos")
	}
	if id, found := images[protosImage]; found == true {
		log.Infof("Found Protos image version '%s'  in your cloud account", protosImage)
		imageID = id
	} else {
		// upload protos image
		if image, found := release.CloudImages["scaleway"]; found {
			log.Info("Latest Protos image not in your infra cloud account. Adding it.")
			imageID, err = client.AddImage(image.URL, image.Digest, release.Version)
			if err != nil {
				return cloud.InstanceInfo{}, errors.Wrap(err, "Failed to initialize Protos")
			}
		} else {
			return cloud.InstanceInfo{}, errors.Errorf("Could not find a Scaleway release for Protos version '%s'", release.Version)
		}
	}

	// create SSH key used for instance
	log.Info("Generating SSH key for the new VM instance")
	key, err := ssh.GenerateKey()
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrap(err, "Failed to initialize Protos")
	}

	// deploy a protos instance
	log.Infof("Deploying Protos instance '%s' using image '%s'", instanceName, imageID)
	vmID, err := client.NewInstance(instanceName, imageID, key.Public())
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrap(err, "Failed to deploy Protos instance")
	}
	log.Infof("Instance with ID '%s' deployed", vmID)

	// get instance info
	instanceInfo, err := client.GetInstanceInfo(vmID)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrap(err, "Failed to get Protos instance info")
	}
	// save of the instance information
	err = dbp.SaveInstance(instanceInfo)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrapf(err, "Failed to save instance '%s'", instanceName)
	}

	// create protos data volume
	log.Infof("Creating data volume for Protos instance '%s'", instanceName)
	volumeID, err := client.NewVolume(instanceName, 30000)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrap(err, "Failed to create data volume")
	}

	// attach volume to instance
	err = client.AttachVolume(volumeID, vmID)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrapf(err, "Failed to attach volume to instance '%s'", instanceName)
	}

	// start protos instance
	log.Infof("Starting Protos instance '%s'", instanceName)
	err = client.StartInstance(vmID)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrap(err, "Failed to start Protos instance")
	}

	// get instance info again
	instanceInfo, err = client.GetInstanceInfo(vmID)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrap(err, "Failed to get Protos instance info")
	}
	// final save of the instance information
	instanceInfo.KeySeed = key.Seed()
	err = dbp.SaveInstance(instanceInfo)
	if err != nil {
		return cloud.InstanceInfo{}, errors.Wrapf(err, "Failed to save instance '%s'", instanceName)
	}

	return instanceInfo, nil
}

func deleteInstance(name string) error {
	instance, err := dbp.GetInstance(name)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve instance '%s'", name)
	}
	cloudInfo, err := dbp.GetCloud(instance.CloudName)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve cloud '%s'", name)
	}
	client := cloudInfo.Client()
	err = client.Init(cloudInfo.Auth, instance.Location)
	if err != nil {
		return errors.Wrapf(err, "Could not init cloud '%s'", name)
	}

	log.Infof("Stopping instance '%s' (%s)", instance.Name, instance.VMID)
	err = client.StopInstance(instance.VMID)
	if err != nil {
		return errors.Wrapf(err, "Could not stop instance '%s'", name)
	}
	vmInfo, err := client.GetInstanceInfo(instance.VMID)
	if err != nil {
		return errors.Wrapf(err, "Failed to get details for instance '%s'", name)
	}
	log.Infof("Deleting instance '%s' (%s)", instance.Name, instance.VMID)
	err = client.DeleteInstance(instance.VMID)
	if err != nil {
		return errors.Wrapf(err, "Could not delete instance '%s'", name)
	}
	for _, vol := range vmInfo.Volumes {
		log.Infof("Deleting volume '%s' (%s) for instance '%s'", vol.Name, vol.VolumeID, name)
		err = client.DeleteVolume(vol.VolumeID)
		if err != nil {
			log.Errorf("Failed to delete volume '%s': %s", vol.Name, err.Error())
		}
	}
	return dbp.DeleteInstance(name)
}

func startInstance(name string) error {
	instance, err := dbp.GetInstance(name)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve instance '%s'", name)
	}
	cloudInfo, err := dbp.GetCloud(instance.CloudName)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve cloud '%s'", name)
	}
	client := cloudInfo.Client()
	err = client.Init(cloudInfo.Auth, instance.Location)
	if err != nil {
		return errors.Wrapf(err, "Could not init cloud '%s'", name)
	}

	log.Infof("Starting instance '%s' (%s)", instance.Name, instance.VMID)
	err = client.StartInstance(instance.VMID)
	if err != nil {
		return errors.Wrapf(err, "Could not start instance '%s'", name)
	}
	return nil
}

func stopInstance(name string) error {
	instance, err := dbp.GetInstance(name)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve instance '%s'", name)
	}
	cloudInfo, err := dbp.GetCloud(instance.CloudName)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve cloud '%s'", name)
	}
	client := cloudInfo.Client()
	err = client.Init(cloudInfo.Auth, instance.Location)
	if err != nil {
		return errors.Wrapf(err, "Could not init cloud '%s'", name)
	}

	log.Infof("Stopping instance '%s' (%s)", instance.Name, instance.VMID)
	err = client.StopInstance(instance.VMID)
	if err != nil {
		return errors.Wrapf(err, "Could not stop instance '%s'", name)
	}
	return nil
}

func tunnelInstance(name string) error {
	instanceInfo, err := dbp.GetInstance(name)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve instance '%s'", name)
	}
	if len(instanceInfo.KeySeed) == 0 {
		return errors.Errorf("Instance '%s' is missing its SSH key", name)
	}
	key, err := ssh.NewKeyFromSeed(instanceInfo.KeySeed)
	if err != nil {
		return errors.Wrapf(err, "Instance '%s' has an invalid SSH key", name)
	}

	log.Infof("Creating SSH tunnel to instance '%s', using ip '%s'", instanceInfo.Name, instanceInfo.PublicIP)
	tunnel := ssh.NewTunnel(instanceInfo.PublicIP+":22", "root", key.SSHAuth(), "localhost:8080", log)
	localPort, err := tunnel.Start()
	if err != nil {
		return errors.Wrap(err, "Error while creating the SSH tunnel")
	}

	quit := make(chan interface{}, 1)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go catchSignals(sigs, quit)

	log.Infof("SSH tunnel ready. Use 'http://localhost:%d/' to access the instance dashboard. Once finished, press CTRL+C to terminate the SSH tunnel", localPort)

	// waiting for a SIGTERM or SIGINT
	<-quit

	log.Info("CTRL+C received. Terminating the SSH tunnel")
	err = tunnel.Close()
	if err != nil {
		return errors.Wrap(err, "Error while terminating the SSH tunnel")
	}
	log.Info("SSH tunnel terminated successfully")
	return nil
}

func keyInstance(name string) error {
	instanceInfo, err := dbp.GetInstance(name)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve instance '%s'", name)
	}
	if len(instanceInfo.KeySeed) == 0 {
		return errors.Errorf("Instance '%s' is missing its SSH key", name)
	}
	key, err := ssh.NewKeyFromSeed(instanceInfo.KeySeed)
	if err != nil {
		return errors.Wrapf(err, "Instance '%s' has an invalid SSH key", name)
	}
	fmt.Print(key.EncodePrivateKeytoPEM())
	return nil
}

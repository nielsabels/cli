package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	survey "github.com/AlecAivazis/survey/v2"
	"github.com/pkg/errors"
	"github.com/protosio/cli/internal/cloud"
	"github.com/urfave/cli/v2"
)

var cmdCloud *cli.Command = &cli.Command{
	Name:  "cloud",
	Usage: "Manage cloud providers",
	Subcommands: []*cli.Command{
		{
			Name:  "ls",
			Usage: "List existing cloud provider accounts",
			Action: func(c *cli.Context) error {
				return listCloudProviders()
			},
		},
		{
			Name:      "add",
			ArgsUsage: "<name>",
			Usage:     "Add a new cloud provider account",
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				_, err := addCloudProvider(name)
				return err
			},
		},
		{
			Name:      "delete",
			ArgsUsage: "<name>",
			Usage:     "Delete an existing cloud provider account",
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				return deleteCloudProvider(name)
			},
		},
		{
			Name:      "info",
			ArgsUsage: "<name>",
			Usage:     "Prints info about cloud provider account and checks if the API is reachable",
			Action: func(c *cli.Context) error {
				name := c.Args().Get(0)
				if name == "" {
					cli.ShowSubcommandHelp(c)
					os.Exit(1)
				}
				return infoCloudProvider(name)
			},
		},
	},
}

//
//  Cloud provider methods
//

func listCloudProviders() error {
	clouds, err := dbp.GetAllClouds()
	if err != nil {
		return err
	}

	w := new(tabwriter.Writer)
	w.Init(os.Stdout, 16, 16, 0, '\t', 0)

	defer w.Flush()

	fmt.Fprintf(w, " %s\t%s\t", "Name", "Type")
	fmt.Fprintf(w, "\n %s\t%s\t", "----", "----")
	for _, cl := range clouds {
		fmt.Fprintf(w, "\n %s\t%s\t", cl.Name, cl.Type)
	}
	fmt.Fprint(w, "\n")
	return nil
}

func addCloudProvider(cloudName string) (cloud.Provider, error) {
	// select cloud provider
	var cloudType string
	cloudProviderSelect := surveySelect(cloud.SupportedProviders(), "Choose one of the following supported cloud providers:")
	err := survey.AskOne(cloudProviderSelect, &cloudType)
	if err != nil {
		return nil, err
	}

	// create new cloud provider
	client, err := cloud.NewProvider(cloudName, cloudType)
	if err != nil {
		return nil, err
	}

	// get cloud provider credentials
	cloudCredentials := map[string]interface{}{}
	credFields := client.AuthFields()
	credentialsQuestions := getCloudCredentialsQuestions(cloudType, credFields)

	err = survey.Ask(credentialsQuestions, &cloudCredentials)
	if err != nil {
		return nil, err
	}

	// init cloud client
	supportedLocations := client.SupportedLocations()
	err = client.Init(transformCredentials(cloudCredentials), supportedLocations[0])
	if err != nil {
		return nil, err
	}

	// save the cloud provider in the db
	cloudProviderInfo := client.GetInfo()
	err = dbp.SaveCloud(cloudProviderInfo)
	if err != nil {
		return nil, errors.Wrap(err, "Failed to save cloud provider info")
	}

	return client, nil
}

func deleteCloudProvider(name string) error {
	return dbp.DeleteCloud(name)
}

func infoCloudProvider(name string) error {
	cloud, err := dbp.GetCloud(name)
	if err != nil {
		return errors.Wrapf(err, "Could not retrieve cloud '%s'", name)
	}
	client := cloud.Client()
	locations := client.SupportedLocations()
	err = client.Init(cloud.Auth, locations[0])
	if err != nil {
		return errors.Wrapf(err, "Failed to connect to cloud provider '%s'(%s) API", name, cloud.Type.String())
	}
	fmt.Printf("Name: %s\n", cloud.Name)
	fmt.Printf("Type: %s\n", cloud.Type.String())
	fmt.Printf("Supported locations: %s\n", strings.Join(locations, " | "))
	if err != nil {
		fmt.Printf("Status: NOT OK (%s)\n", err.Error())
	} else {
		fmt.Printf("Status: OK - API reachable\n")
	}
	return nil
}

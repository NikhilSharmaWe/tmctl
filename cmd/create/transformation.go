/*
Copyright 2022 TriggerMesh Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package create

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/triggermesh/tmctl/pkg/completion"
	"github.com/triggermesh/tmctl/pkg/output"
	"github.com/triggermesh/tmctl/pkg/triggermesh"
	"github.com/triggermesh/tmctl/pkg/triggermesh/components"
	tmbroker "github.com/triggermesh/tmctl/pkg/triggermesh/components/broker"
	"github.com/triggermesh/tmctl/pkg/triggermesh/components/transformation"
)

const (
	helpColorCode    = "\033[90m"
	defaultColorCode = "\033[39m"
	helpText         = `Transformation example:

context:
- operation: add
  paths:
  - key: source
    value: some-test-source
data:
- operation: store
  paths:
  - key: $foo
    value: Body
- operation: delete
  paths:
  - key:
- operation: add
  paths:
  - key: foo
    value: $foo

For more samples please visit:
https://github.com/triggermesh/triggermesh/tree/main/config/samples/bumblebee`
)

func (o *CreateOptions) NewTransformationCmd() *cobra.Command {
	var name, target, file string
	var eventSourcesFilter, eventTypesFilter []string
	transformationCmd := &cobra.Command{
		Use: "transformation [--target <name>][--source <name>,<name>...][--eventTypes <type>,<type>...][--from <path>]",
		// Short:     "TriggerMesh transformation",
		ValidArgs: []string{"--name", "--target", "--source", "--eventTypes", "--from"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cobra.CheckErr(o.Manifest.Read())
			return o.transformation(name, target, file, eventSourcesFilter, eventTypesFilter)
		},
	}
	transformationCmd.Flags().StringVar(&name, "name", "", "Transformation name")
	transformationCmd.Flags().StringVarP(&file, "from", "f", "", "Transformation specification file")
	transformationCmd.Flags().StringVar(&target, "target", "", "Target name")
	transformationCmd.Flags().StringSliceVar(&eventSourcesFilter, "source", []string{}, "Sources component names")
	transformationCmd.Flags().StringSliceVar(&eventTypesFilter, "eventTypes", []string{}, "Event types filter")

	transformationCmd.RegisterFlagCompletionFunc("name", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{}, cobra.ShellCompDirectiveNoFileComp
	})
	transformationCmd.RegisterFlagCompletionFunc("source", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return completion.ListSources(o.Manifest), cobra.ShellCompDirectiveNoFileComp
	})
	transformationCmd.RegisterFlagCompletionFunc("eventTypes", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return completion.ListEventTypes(o.Manifest, o.CRD), cobra.ShellCompDirectiveNoFileComp
	})
	transformationCmd.RegisterFlagCompletionFunc("target", func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		return completion.ListTargets(o.Manifest), cobra.ShellCompDirectiveNoFileComp
	})
	return transformationCmd
}

func (o *CreateOptions) transformation(name, target, file string, eventSourcesFilter, eventTypesFilter []string) error {
	ctx := context.Background()
	var targetPort string
	if target != "" {
		port, err := o.lookupTarget(ctx, target)
		if err != nil {
			return err
		}
		targetPort = port
	}

	eventSourcesFilter, err := o.translateEventSource(eventSourcesFilter)
	if err != nil {
		return err
	}

	var data []byte
	if file == "" {
		input, err := fromStdIn()
		if err != nil {
			return fmt.Errorf("spec read: %w", err)
		}
		data = []byte(input)
	} else {
		specFile, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("spec file read: %w", err)
		}
		data = specFile
	}
	var spec map[string]interface{}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("decode spec: %w", err)
	}

	t := transformation.New(name, o.CRD, "transformation", o.Context, o.Version, spec)

	transformationEventType := fmt.Sprintf("%s.output", t.GetName())
	if et, _ := t.(triggermesh.Producer).GetEventTypes(); len(et) == 0 {
		if err := t.(triggermesh.Producer).SetEventAttributes(map[string]string{
			"type": transformationEventType,
		}); err != nil {
			return fmt.Errorf("setting event type: %w", err)
		}
	} else {
		transformationEventType = et[0]
	}
	log.Println("Updating manifest")
	restart, err := o.Manifest.Add(t)
	if err != nil {
		return fmt.Errorf("unable to update manifest: %w", err)
	}

	log.Println("Starting container")
	container, err := t.(triggermesh.Runnable).Start(ctx, nil, restart)
	if err != nil {
		return err
	}

	var targetTriggers []triggermesh.Component
	// creating new trigger from transformation to target
	if target != "" {
		if targetTriggers, err = tmbroker.GetTargetTriggers(o.Context, o.ConfigBase, target); err != nil {
			return fmt.Errorf("target triggers: %w", err)
		}
		if _, err := o.createTrigger("", targetPort, target, tmbroker.FilterExactAttribute("type", transformationEventType)); err != nil {
			return fmt.Errorf("create trigger: %w", err)
		}
	}

	// updating existing triggers from sources to target
	for _, et := range eventTypesFilter {
		filter := tmbroker.FilterExactAttribute("type", et)
		if _, err := o.createTrigger("", container.HostPort(), container.Name, filter); err != nil {
			return err
		}
		for _, component := range targetTriggers {
			trigger := component.(*tmbroker.Trigger)
			if len(trigger.Filters) != 1 || &trigger.Filters[0] != &filter {
				continue
			}
			if err := trigger.RemoveTriggerFromConfig(); err != nil {
				return err
			}
			if err := o.Manifest.Remove(trigger.GetName(), trigger.GetKind()); err != nil {
				return err
			}
		}
	}

	for _, es := range eventSourcesFilter {
		filter := tmbroker.FilterExactAttribute("source", es)
		if _, err := o.createTrigger("", container.HostPort(), container.Name, filter); err != nil {
			return err
		}
		for _, component := range targetTriggers {
			trigger := component.(*tmbroker.Trigger)
			if len(trigger.Filters) != 1 || &trigger.Filters[0] != &filter {
				continue
			}
			if err := trigger.RemoveTriggerFromConfig(); err != nil {
				return err
			}
			if err := o.Manifest.Remove(trigger.GetName(), trigger.GetKind()); err != nil {
				return err
			}
		}
	}

	if len(eventTypesFilter) == 0 && len(eventSourcesFilter) == 0 {
		for _, trigger := range targetTriggers {
			trigger.(*tmbroker.Trigger).SetTarget(container.Name, fmt.Sprintf("http://host.docker.internal:%s", container.HostPort()))
			if err := trigger.(*tmbroker.Trigger).UpdateBrokerConfig(); err != nil {
				return err
			}
			if _, err := o.Manifest.Add(trigger); err != nil {
				return err
			}
		}
	}
	output.PrintStatus("consumer", t, eventSourcesFilter, eventTypesFilter)
	return nil
}

func fromStdIn() (string, error) {
	fmt.Printf("%s%s%s\n\n", helpColorCode, helpText, defaultColorCode)
	fmt.Printf("Insert Bumblebee transformation below\nPress Enter key twice to finish:\n")
	input, err := readInput()
	if err != nil {
		return "", fmt.Errorf("input read: %w", err)
	}
	input = strings.TrimRight(input, "\n")
	input = strings.TrimLeft(input, "\n")
	return input, nil
}

func readInput() (string, error) {
	var lines string
	scn := bufio.NewScanner(os.Stdin)
	for scn.Scan() {
		line := scn.Text()
		if len(line) == 0 {
			break
		}
		lines = fmt.Sprintf("%s\n%s", lines, line)
	}
	return lines, scn.Err()
}

func (o *CreateOptions) lookupTarget(ctx context.Context, target string) (string, error) {
	targetObject, err := components.GetObject(target, o.CRD, o.Version, o.Manifest)
	if err != nil {
		return "", fmt.Errorf("transformation target: %w", err)
	}
	consumer, ok := targetObject.(triggermesh.Consumer)
	if !ok {
		return "", fmt.Errorf("%q is not an event consumer", target)
	}
	return consumer.GetPort(ctx)
}

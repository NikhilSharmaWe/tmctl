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

package triggermesh

import (
	"context"

	"github.com/triggermesh/tmcli/pkg/docker"
	"github.com/triggermesh/tmcli/pkg/kubernetes"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Component interface {
	AsUnstructured() (*unstructured.Unstructured, error)
	AsK8sObject() (*kubernetes.Object, error)

	GetName() string
	GetKind() string
}

type Runnable interface {
	AsContainer() (*docker.Container, error)

	GetImage() string
}

type Producer interface {
	SetEventType(string) error
	GetEventTypes() ([]string, error)
}

type Consumer interface {
	ConsumedEventTypes() ([]string, error)
	GetPort(context.Context) (string, error)
}

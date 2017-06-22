// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package registry contains types and functions for registry server
// interaction.
package registry

import (
	"github.com/pkg/errors"
	"github.com/tsuru/config"
)

const defaultRegistry = "docker"

// Registry is the basic interface of this package
type Registry interface {
	RemoveAppImages(appName string) error
}

// RemoveAppImages is a wrapper to the same method in the defined registry.
func RemoveAppImages(appName string) error {
	reg, err := GetRegistry()
	if err != nil {
		return err
	}
	return reg.RemoveAppImages(appName)
}

var registries = make(map[string]Registry)

// Register registers a new registry
func Register(name string, reg Registry) {
	registries[name] = reg
}

// GetRegistry returns the registry defined in the configuration file,
// or the default if not defined.
func GetRegistry() (Registry, error) {
	name, err := config.GetString("registry")
	if err != nil {
		name = defaultRegistry
	}
	if _, ok := registries[name]; !ok {
		return nil, errors.Errorf("unknow registry: %s", name)
	}
	return registries[name], nil
}

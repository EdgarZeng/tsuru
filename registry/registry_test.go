// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/registry/fake"
	check "gopkg.in/check.v1"
)

func Test(t *testing.T) { check.TestingT(t) }

type S struct{}

var _ = check.Suite(&S{})

func (s *S) TestRegister(c *check.C) {
	var r fake.FakeRegistry
	Register("fake", &r)
	defer func() {
		delete(registries, "fake")
	}()
	c.Assert(registries["fake"], check.Equals, &r)
}

func (s *S) TestConfigRegistry(c *check.C) {
	var r fake.FakeRegistry
	Register("fake", &r)
	config.Set("registry", "fake")
	defer config.Unset("registry")
	current, err := GetRegistry()
	c.Assert(err, check.IsNil)
	c.Assert(current, check.Equals, &r)
}

func (s *S) TestGetDefaultRegistry(c *check.C) {
	var r fake.FakeRegistry
	Register("fake", &r)
	var docker fake.FakeRegistry
	Register("docker", &docker)
	config.Unset("registry")
	current, err := GetRegistry()
	c.Assert(err, check.IsNil)
	c.Assert(current, check.Equals, &docker)
}

func (s *S) TestConfigUnknownRegistry(c *check.C) {
	config.Set("registry", "something")
	defer config.Unset("registry")
	_, err := GetRegistry()
	c.Assert(err, check.NotNil)
}

func (s *S) TestRemoveAppImages(c *check.C) {
	var r fake.FakeRegistry
	Register("fake", &r)
	config.Set("registry", "fake")
	defer config.Unset("registry")
	err := RemoveAppImages("teste")
	c.Assert(err, check.IsNil)
	c.Assert(r.RemoveAppImagesCalled, check.Equals, true)
}

// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fake

import (
	"testing"

	check "gopkg.in/check.v1"
)

func Test(t *testing.T) { check.TestingT(t) }

type S struct{}

var _ = check.Suite(&S{})

func (s *S) TestRemoveAppImages(c *check.C) {
	var r FakeRegistry
	err := r.RemoveAppImages("teste")
	c.Assert(err, check.IsNil)
	c.Assert(r.RemoveAppImagesCalled, check.Equals, true)
}

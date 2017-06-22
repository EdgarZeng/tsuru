// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fake

type FakeRegistry struct {
	RemoveAppImagesCalled bool
}

func (r *FakeRegistry) RemoveAppImages(appName string) error {
	r.RemoveAppImagesCalled = true
	return nil
}

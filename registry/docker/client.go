// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package docker

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/tsuru/config"
)

func (r *dockerRegistry) doRequest(method, path string, headers map[string]string) (*http.Response, error) {
	endpoint := fmt.Sprintf("http://%s%s", r.server, path)
	req, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (r *dockerRegistry) client() (err error) {
	r.server, err = config.GetString("docker:registry")
	if err != nil {
		return err
	}
	dialTimeout := 10 * time.Second
	fullTimeout := 5 * time.Minute
	transport := http.Transport{
		Dial: (&net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSHandshakeTimeout: dialTimeout,
		MaxIdleConnsPerHost: -1,
		DisableKeepAlives:   true,
		TLSClientConfig:     nil,
	}
	r.HTTPClient = &http.Client{
		Transport: &transport,
		Timeout:   fullTimeout,
	}
	return nil
}

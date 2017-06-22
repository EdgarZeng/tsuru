// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package docker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tsuru/tsuru/registry"
)

func init() {
	registry.Register("docker", &dockerRegistry{})
}

type dockerRegistry struct {
	HTTPClient *http.Client
	server     string
}

// RemoveAppImages removes an image manifest from a remote registry v2 server, returning an error
// in case of failure.
func (r *dockerRegistry) RemoveAppImages(appName string) error {
	image := fmt.Sprintf("tsuru/app-%s", appName)
	err := r.client()
	if err != nil {
		return err
	}
	tags, err := r.getImageTags(image)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		digest, err := r.getDigest(image, tag)
		if err != nil {
			fmt.Printf("failed to get digest for image %s/%s:%s on registry: %v\n", r.server, image, tag, err)
			continue
		}
		err = r.removeImage(image, digest)
		if err != nil {
			if strings.Contains(err.Error(), "405") {
				return err
			}
			fmt.Printf("failed to remove image %s/%s:%s/%s on registry: %v\n", r.server, image, tag, digest, err)
		}
	}
	return nil
}

func (r dockerRegistry) getDigest(image, tag string) (string, error) {
	path := fmt.Sprintf("/v2/%s/manifests/%s", image, tag)
	resp, err := r.doRequest("HEAD", path, map[string]string{"Accept": "application/vnd.docker.distribution.manifest.v2+json"})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return "", fmt.Errorf("manifest not found (%d)", resp.StatusCode)
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}

type imageTags struct {
	Name string
	Tags []string
}

func (r dockerRegistry) getImageTags(image string) ([]string, error) {
	path := fmt.Sprintf("/v2/%s/tags/list", image)
	resp, err := r.doRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("image not found (%d)", resp.StatusCode)
	}
	var it imageTags
	if err := json.NewDecoder(resp.Body).Decode(&it); err != nil {
		return nil, err
	}
	return it.Tags, nil
}

func (r dockerRegistry) removeImage(image, digest string) error {
	path := fmt.Sprintf("/v2/%s/manifests/%s", image, digest)
	resp, err := r.doRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return fmt.Errorf("repository not found (%d)", resp.StatusCode)
	}
	if resp.StatusCode == 405 {
		return fmt.Errorf("storage delete is disabled (%d)", resp.StatusCode)
	}
	return nil
}

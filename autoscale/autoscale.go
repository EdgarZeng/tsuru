// Copyright 2015 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package autoscale

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/api/shutdown"
	"github.com/tsuru/tsuru/app"
	tsuruErrors "github.com/tsuru/tsuru/errors"
	"github.com/tsuru/tsuru/event"
	"github.com/tsuru/tsuru/iaas"
	"github.com/tsuru/tsuru/log"
	"github.com/tsuru/tsuru/net"
	"github.com/tsuru/tsuru/permission"
	"github.com/tsuru/tsuru/provision"
	"github.com/tsuru/tsuru/safe"
	"gopkg.in/mgo.v2"
)

const (
	EventKind = "autoscale"
)

var globalConfig *Config

type Config struct {
	WaitTimeNewMachine  time.Duration
	RunInterval         time.Duration
	TotalMemoryMetadata string
	done                chan bool
	writer              io.Writer
	running             bool
}

func CurrentConfig() (*Config, error) {
	if globalConfig == nil {
		return nil, errors.New("autoscale not initialized")
	}
	return globalConfig, nil
}

func Initialize() error {
	globalConfig = newConfig()
	shutdown.Register(globalConfig)
	go globalConfig.run()
	return nil
}

func RunOnce(w io.Writer) error {
	conf := newConfig()
	conf.writer = w
	return conf.runOnce()
}

func newConfig() *Config {
	waitSecondsNewMachine, _ := config.GetInt("docker:auto-scale:wait-new-time")
	runInterval, _ := config.GetInt("docker:auto-scale:run-interval")
	totalMemoryMetadata, _ := config.GetString("docker:scheduler:total-memory-metadata")
	c := &Config{
		TotalMemoryMetadata: totalMemoryMetadata,
		WaitTimeNewMachine:  time.Duration(waitSecondsNewMachine) * time.Second,
		RunInterval:         time.Duration(runInterval) * time.Second,
		done:                make(chan bool),
	}
	if c.RunInterval == 0 {
		c.RunInterval = time.Hour
	}
	if c.WaitTimeNewMachine == 0 {
		c.WaitTimeNewMachine = 5 * time.Minute
	}
	return c
}

type errAppNotLocked struct {
	app string
}

func (e errAppNotLocked) Error() string {
	return fmt.Sprintf("unable to lock app %q", e.app)
}

type ScalerResult struct {
	ToAdd       int
	ToRemove    []provision.NodeSpec
	ToRebalance bool
	Reason      string
}

func (r *ScalerResult) IsRebalanceOnly() bool {
	return r.ToAdd == 0 && len(r.ToRemove) == 0 && r.ToRebalance
}

func (r *ScalerResult) NoAction() bool {
	return r.ToAdd == 0 && len(r.ToRemove) == 0 && !r.ToRebalance
}

type autoScaler interface {
	scale(pool string, nodes []provision.Node) (*ScalerResult, error)
}

func (a *Config) scalerForRule(rule *Rule) (autoScaler, error) {
	if rule.MaxContainerCount > 0 {
		return &countScaler{Config: a, rule: rule}, nil
	}
	return &memoryScaler{Config: a, rule: rule}, nil
}

func (a *Config) run() error {
	a.running = true
	for {
		err := a.runScaler()
		if err != nil {
			a.logError(err.Error())
			err = errors.Wrap(err, "[node autoscale]")
		}
		select {
		case <-a.done:
			return err
		case <-time.After(a.RunInterval):
		}
	}
}

func (a *Config) logError(msg string, params ...interface{}) {
	msg = fmt.Sprintf("[node autoscale] %s", msg)
	log.Errorf(msg, params...)
}

func (a *Config) logDebug(msg string, params ...interface{}) {
	msg = fmt.Sprintf("[node autoscale] %s", msg)
	log.Debugf(msg, params...)
}

func (a *Config) runOnce() error {
	err := a.runScaler()
	if err != nil {
		a.logError(err.Error())
	}
	return err
}

func (a *Config) Shutdown(ctx context.Context) error {
	if !a.running {
		return nil
	}
	a.done <- true
	a.running = false
	return nil
}

func (a *Config) String() string {
	return "node auto scale"
}

func (a *Config) runScaler() (retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = errors.Errorf("recovered panic, we can never stop! panic: %v", r)
		}
	}()
	provs, err := provision.Registry()
	if err != nil {
		return errors.Wrap(err, "error getting provisioners")
	}
	provPoolMap := map[string]provision.NodeProvisioner{}
	var allNodes []provision.Node
	for _, prov := range provs {
		nodeProv, ok := prov.(provision.NodeProvisioner)
		if !ok {
			continue
		}
		var nodes []provision.Node
		nodes, err = nodeProv.ListNodes(nil)
		if err != nil {
			a.logDebug("skipped provisioner, error getting nodes: %v", err)
			continue
		}
		for _, n := range nodes {
			provPoolMap[n.Pool()] = nodeProv
		}
		allNodes = append(allNodes, nodes...)
	}
	clusterMap := map[string][]provision.Node{}
	for _, node := range allNodes {
		pool := node.Pool()
		if pool == "" {
			a.logDebug("skipped node %s, no pool value found.", node.Address)
			continue
		}
		clusterMap[pool] = append(clusterMap[pool], node)
	}
	for pool, nodes := range clusterMap {
		a.runScalerInNodes(provPoolMap[pool], pool, nodes)
	}
	return
}

type EventCustomData struct {
	Result *ScalerResult
	Nodes  []provision.NodeSpec
	Rule   *Rule
}

func nodesToSpec(nodes []provision.Node) []provision.NodeSpec {
	var nodeSpecs []provision.NodeSpec
	for _, n := range nodes {
		nodeSpecs = append(nodeSpecs, provision.NodeToSpec(n))
	}
	return nodeSpecs
}

func (a *Config) runScalerInNodes(prov provision.NodeProvisioner, pool string, nodes []provision.Node) {
	evt, err := event.NewInternal(&event.Opts{
		Target:       event.Target{Type: event.TargetTypePool, Value: pool},
		InternalKind: EventKind,
		Allowed:      event.Allowed(permission.PermPoolReadEvents, permission.Context(permission.CtxPool, pool)),
	})
	if err != nil {
		if _, ok := err.(event.ErrEventLocked); ok {
			a.logDebug("skipping already running for: %s", pool)
		} else {
			a.logError("error creating scale event %s: %s", pool, err.Error())
		}
		return
	}
	evt.SetLogWriter(a.writer)
	var retErr error
	var sResult *ScalerResult
	var evtNodes []provision.NodeSpec
	var rule *Rule
	defer func() {
		if retErr != nil {
			evt.Logf(retErr.Error())
		}
		if (sResult == nil && retErr == nil) || (sResult != nil && sResult.NoAction()) {
			evt.Logf("nothing to do for %q: %q", provision.PoolMetadataName, pool)
			evt.Abort()
		} else {
			evt.DoneCustomData(retErr, EventCustomData{
				Result: sResult,
				Nodes:  evtNodes,
				Rule:   rule,
			})
		}
	}()
	rule, err = AutoScaleRuleForMetadata(pool)
	if err == mgo.ErrNotFound {
		rule, err = AutoScaleRuleForMetadata("")
	}
	if err != nil {
		if err != mgo.ErrNotFound {
			retErr = errors.Wrapf(err, "unable to fetch auto scale rules for %s", pool)
			return
		}
		evt.Logf("no auto scale rule for %s", pool)
		return
	}
	if !rule.Enabled {
		evt.Logf("auto scale rule disabled for %s", pool)
		return
	}
	scaler, err := a.scalerForRule(rule)
	if err != nil {
		retErr = errors.Wrapf(err, "error getting scaler for %s", pool)
		return
	}
	evt.Logf("running scaler %T for %q: %q", scaler, provision.PoolMetadataName, pool)
	sResult, err = scaler.scale(pool, nodes)
	if err != nil {
		if _, ok := err.(errAppNotLocked); ok {
			evt.Logf("aborting scaler for now, gonna retry later: %s", err)
			return
		}
		retErr = errors.Wrapf(err, "error scaling group %s", pool)
		return
	}
	if sResult.ToAdd > 0 {
		evt.Logf("running event \"add\" for %q: %#v", pool, sResult)
		evtNodes, err = a.addMultipleNodes(evt, prov, nodes, sResult.ToAdd)
		if err != nil {
			if len(evtNodes) == 0 {
				retErr = err
				return
			}
			evt.Logf("not all required nodes were created: %s", err)
		}
	} else if len(sResult.ToRemove) > 0 {
		evt.Logf("running event \"remove\" for %q: %#v", pool, sResult)
		evtNodes = sResult.ToRemove
		err = a.removeMultipleNodes(evt, prov, sResult.ToRemove)
		if err != nil {
			retErr = err
			return
		}
	}
	if !rule.PreventRebalance {
		err := a.rebalanceIfNeeded(evt, prov, pool, nodes, sResult)
		if err != nil {
			if sResult.IsRebalanceOnly() {
				retErr = err
			} else {
				evt.Logf("unable to rebalance: %s", err.Error())
			}
		}
	}
}

func (a *Config) rebalanceIfNeeded(evt *event.Event, prov provision.NodeProvisioner, pool string, nodes []provision.Node, sResult *ScalerResult) error {
	if len(sResult.ToRemove) > 0 {
		return nil
	}
	rebalanceProv, ok := prov.(provision.NodeRebalanceProvisioner)
	if !ok {
		return nil
	}
	buf := safe.NewBuffer(nil)
	writer := io.MultiWriter(buf, evt)
	shouldRebalance, err := rebalanceProv.RebalanceNodes(provision.RebalanceNodesOptions{
		Force:          false,
		MetadataFilter: map[string]string{provision.PoolMetadataName: pool},
		Writer:         writer,
	})
	sResult.ToRebalance = shouldRebalance
	if err != nil {
		return errors.Wrapf(err, "unable to rebalance containers. log: %s", buf.String())
	}
	return nil
}

func (a *Config) addMultipleNodes(evt *event.Event, prov provision.NodeProvisioner, modelNodes []provision.Node, count int) ([]provision.NodeSpec, error) {
	wg := sync.WaitGroup{}
	wg.Add(count)
	nodesCh := make(chan provision.Node, count)
	errCh := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			defer wg.Done()
			node, err := a.addNode(evt, prov, modelNodes)
			if err != nil {
				errCh <- err
				return
			}
			nodesCh <- node
		}()
	}
	wg.Wait()
	close(nodesCh)
	close(errCh)
	var nodes []provision.NodeSpec
	for n := range nodesCh {
		nodes = append(nodes, provision.NodeToSpec(n))
	}
	return nodes, <-errCh
}

func (a *Config) addNode(evt *event.Event, prov provision.NodeProvisioner, modelNodes []provision.Node) (provision.Node, error) {
	metadata, err := chooseMetadataFromNodes(modelNodes)
	if err != nil {
		return nil, err
	}
	_, hasIaas := metadata["iaas"]
	if !hasIaas {
		return nil, errors.Errorf("no IaaS information in nodes metadata: %#v", metadata)
	}
	machine, err := iaas.CreateMachineForIaaS(metadata["iaas"], metadata)
	if err != nil {
		return nil, errors.Wrap(err, "unable to create machine")
	}
	newAddr := machine.FormatNodeAddress()
	evt.Logf("new machine created: %s - Waiting for docker to start...", newAddr)
	createOpts := provision.AddNodeOptions{
		Address:    newAddr,
		Metadata:   metadata,
		WaitTO:     a.WaitTimeNewMachine,
		CaCert:     machine.CaCert,
		ClientCert: machine.ClientCert,
		ClientKey:  machine.ClientKey,
	}
	err = prov.AddNode(createOpts)
	if err != nil {
		return nil, errors.Wrapf(err, "error adding new node %s", newAddr)
	}
	evt.Logf("new machine created: %s - started!", newAddr)
	node, err := prov.GetNode(newAddr)
	if err != nil {
		return nil, errors.Wrapf(err, "error getting new node %s", newAddr)
	}
	return node, nil
}

func (a *Config) removeMultipleNodes(evt *event.Event, prov provision.NodeProvisioner, chosenNodes []provision.NodeSpec) error {
	nodeAddrs := make([]string, len(chosenNodes))
	nodeHosts := make([]string, len(chosenNodes))
	for i, node := range chosenNodes {
		_, hasIaas := node.Metadata["iaas"]
		if !hasIaas {
			return errors.Errorf("no IaaS information in node (%s) metadata: %#v", node.Address, node.Metadata)
		}
		nodeAddrs[i] = node.Address
		nodeHosts[i] = net.URLToHost(node.Address)
	}
	errCh := make(chan error, len(chosenNodes))
	wg := sync.WaitGroup{}
	for i := range chosenNodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			node := chosenNodes[i]
			buf := safe.NewBuffer(nil)
			err := prov.RemoveNode(provision.RemoveNodeOptions{
				Address:   node.Address,
				Writer:    buf,
				Rebalance: true,
			})
			if err != nil {
				errCh <- errors.Wrapf(err, "unable to unregister node %s for removal", node.Address)
				return
			}
			m, err := iaas.FindMachineByIdOrAddress(node.Metadata["iaas-id"], net.URLToHost(node.Address))
			if err != nil {
				evt.Logf("unable to find machine for removal in iaas: %s", err)
				return
			}
			err = m.Destroy()
			if err != nil {
				evt.Logf("unable to destroy machine in IaaS: %s", err)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	multiErr := tsuruErrors.NewMultiError()
	for err := range errCh {
		multiErr.Add(err)
	}
	if multiErr.Len() > 0 {
		return multiErr
	}
	return nil
}

func chooseNodeForRemoval(nodes []provision.Node, toRemoveCount int) []provision.Node {
	var chosenNodes []provision.Node
	remainingNodes := nodes[:]
	for _, node := range nodes {
		canRemove, _ := canRemoveNode(node, remainingNodes)
		if canRemove {
			for i := range remainingNodes {
				if remainingNodes[i].Address() == node.Address() {
					remainingNodes = append(remainingNodes[:i], remainingNodes[i+1:]...)
					break
				}
			}
			chosenNodes = append(chosenNodes, node)
			if len(chosenNodes) >= toRemoveCount {
				break
			}
		}
	}
	return chosenNodes
}

func canRemoveNode(chosenNode provision.Node, nodes []provision.Node) (bool, error) {
	if len(nodes) == 1 {
		return false, nil
	}
	exclusiveList, _, err := provision.NodeList(nodes).SplitMetadata()
	if err != nil {
		return false, err
	}
	if len(exclusiveList) == 0 {
		return true, nil
	}
	hasMetadata := func(n provision.Node, meta map[string]string) bool {
		metadata := n.Metadata()
		for k, v := range meta {
			if metadata[k] != v {
				return false
			}
		}
		return true
	}
	for _, item := range exclusiveList {
		if hasMetadata(chosenNode, item.Metadata) {
			if len(item.Nodes) > 1 {
				return true, nil
			}
			return false, nil
		}
	}
	return false, nil
}

func chooseMetadataFromNodes(modelNodes []provision.Node) (map[string]string, error) {
	exclusiveList, baseMetadata, err := provision.NodeList(modelNodes).SplitMetadata()
	if err != nil {
		return nil, err
	}
	var chosenExclusive map[string]string
	if exclusiveList != nil {
		chosenExclusive = exclusiveList[0].Metadata
	}
	for k, v := range chosenExclusive {
		baseMetadata[k] = v
	}
	return baseMetadata, nil
}

func preciseUnitsByNode(pool string, nodes []provision.Node) (map[string][]provision.Unit, error) {
	appsInPool, err := app.List(&app.Filter{
		Pool: pool,
	})
	if err != nil {
		return nil, err
	}
	for _, a := range appsInPool {
		var locked bool
		locked, err = app.AcquireApplicationLock(a.Name, app.InternalAppName, "node auto scale")
		if err != nil {
			return nil, err
		}
		if !locked {
			return nil, errAppNotLocked{app: a.Name}
		}
		defer app.ReleaseApplicationLock(a.Name)
	}
	unitsByNode := map[string][]provision.Unit{}
	for _, node := range nodes {
		var nodeUnits []provision.Unit
		nodeUnits, err = node.Units()
		if err != nil {
			return nil, err
		}
		unitsByNode[node.Address()] = nodeUnits
	}
	return unitsByNode, err
}

func unitsGapInNodes(pool string, nodes []provision.Node) (int, int, error) {
	maxCount := 0
	minCount := -1
	totalCount := 0
	unitsByNode, err := preciseUnitsByNode(pool, nodes)
	if err != nil {
		return 0, 0, err
	}
	for _, units := range unitsByNode {
		unitCount := len(units)
		if unitCount > maxCount {
			maxCount = unitCount
		}
		if minCount == -1 || unitCount < minCount {
			minCount = unitCount
		}
		totalCount += unitCount
	}
	return totalCount, maxCount - minCount, nil
}

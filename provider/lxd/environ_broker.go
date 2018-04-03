// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// +build go1.3

package lxd

import (
	"fmt"
	"strings"

	"github.com/juju/errors"
	"github.com/juju/utils/arch"
	"github.com/lxc/lxd/client"
	"github.com/lxc/lxd/shared/api"

	"github.com/juju/juju/cloudconfig/cloudinit"
	"github.com/juju/juju/cloudconfig/instancecfg"
	"github.com/juju/juju/cloudconfig/providerinit"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/environs/tags"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/status"
	"github.com/juju/juju/tools"
	"github.com/juju/juju/tools/lxdclient"
	"github.com/juju/juju/tools/lxdtools"
)

// MaintainInstance is specified in the InstanceBroker interface.
func (*environ) MaintainInstance(args environs.StartInstanceParams) error {
	return nil
}

// StartInstance implements environs.InstanceBroker.
func (env *environ) StartInstance(args environs.StartInstanceParams) (*environs.StartInstanceResult, error) {
	// Start a new instance.

	series := args.Tools.OneSeries()
	logger.Debugf("StartInstance: %q, %s", args.InstanceConfig.MachineId, series)

	arch, err := env.finishInstanceConfig(args)
	if err != nil {
		return nil, errors.Trace(err)
	}

	// TODO(ericsnow) Handle constraints?

	lxdinstance, err := env.newRawInstance(args, arch)
	if err != nil {
		if args.StatusCallback != nil {
			args.StatusCallback(status.ProvisioningError, err.Error(), nil)
		}
		return nil, errors.Trace(err)
	}
	logger.Infof("started instance %q", raw.Name)
	inst := newInstance(lxdinstance, env)

	// Build the result.
	hwc := env.getHardwareCharacteristics(args, inst)
	result := environs.StartInstanceResult{
		Instance: inst,
		Hardware: hwc,
	}
	return &result, nil
}

func (env *environ) finishInstanceConfig(args environs.StartInstanceParams) (string, error) {
	// TODO(natefinch): This is only correct so long as the lxd is running on
	// the local machine.  If/when we support a remote lxd environment, we'll
	// need to change this to match the arch of the remote machine.
	arch := arch.HostArch()
	tools, err := args.Tools.Match(tools.Filter{Arch: arch})
	if err != nil {
		return "", errors.Trace(err)
	}
	if err := args.InstanceConfig.SetTools(tools); err != nil {
		return "", errors.Trace(err)
	}
	if err := instancecfg.FinishInstanceConfig(args.InstanceConfig, env.ecfg.Config); err != nil {
		return "", errors.Trace(err)
	}
	return arch, nil
}

func (env *environ) getImageSources() ([]lxdtools.RemoteServer, error) {
	metadataSources, err := environs.ImageMetadataSources(env)
	if err != nil {
		return nil, errors.Trace(err)
	}
	var remotes []lxdtools.RemoteServer
	for _, source := range metadataSources {
		url, err := source.URL("")
		if err != nil {
			logger.Debugf("failed to get the URL for metadataSource: %s", err)
			continue
		}
		// NOTE(jam) LXD only allows you to pass HTTPS URLs. So strip
		// off http:// and replace it with https://
		// Arguably we could give the user a direct error if
		// env.ImageMetadataURL is http instead of https, but we also
		// get http from the DefaultImageSources, which is why we
		// replace it.
		// TODO(jam) Maybe we could add a Validate step that ensures
		// image-metadata-url is an "https://" URL, so that Users get a
		// "your configuration is wrong" error, rather than silently
		// changing it and having them get confused.
		// https://github.com/lxc/lxd/issues/1763
		if strings.HasPrefix(url, "http://") {
			url = strings.TrimPrefix(url, "http://")
			url = "https://" + url
			logger.Debugf("LXD requires https://, using: %s", url)
		}
		remotes = append(remotes, lxdtools.RemoteServer{
			Name:     source.Description(),
			Host:     url,
			Protocol: lxdtools.SimplestreamsProtocol,
		})
	}
	return remotes, nil
}

// newRawInstance is where the new physical instance is actually
// provisioned, relative to the provided args and spec. Info for that
// low-level instance is returned.
func (env *environ) newRawInstance(
	args environs.StartInstanceParams,
	arch string,
) (*lxdInstance, error) {
	hostname, err := env.namespace.Hostname(args.InstanceConfig.MachineId)
	if err != nil {
		return nil, errors.Trace(err)
	}

	// Note: other providers have the ImageMetadata already read for them
	// and passed in as args.ImageMetadata. However, lxd provider doesn't
	// use datatype: image-ids, it uses datatype: image-download, and we
	// don't have a registered cloud/region.
	imageSources, err := env.getImageSources()
	if err != nil {
		return nil, errors.Trace(err)
	}

	// TODO: support args.Constraints.Arch, we'll want to map from

	// Keep track of StatusCallback output so we may clean up later.
	// This is implemented here, close to where the StatusCallback calls
	// are made, instead of at a higher level in the package, so as not to
	// assume that all providers will have the same need to be implemented
	// in the same way.
	longestMsg := 0
	statusCallback := func(currentStatus status.Status, msg string) {
		if args.StatusCallback != nil {
			args.StatusCallback(currentStatus, msg, nil)
		}
		if len(msg) > longestMsg {
			longestMsg = len(msg)
		}
	}
	cleanupCallback := func() {
		if args.CleanupCallback != nil {
			args.CleanupCallback(strings.Repeat(" ", longestMsg))
		}
	}
	defer cleanupCallback()

	series := args.InstanceConfig.Series
	imageServer, image, target, err := lxdtools.GetImageWithServer(env.client, series, arch, imageSources)
	if err != nil {
		return nil, errors.Trace(err)
	}
	cleanupCallback() // Clean out any long line of completed download status

	cloudcfg, err := cloudinit.New(series)
	if err != nil {
		return nil, errors.Trace(err)
	}

	metadata, err := getMetadata(cloudcfg, args)
	if err != nil {
		return nil, errors.Trace(err)
	}

	logger.Infof("starting instance %q (image %q)...", hostname, target)

	statusCallback(status.Allocating, "preparing image")
	profiles := []string{"default", env.profileName()}
/*	nics := make(map[string]map[string]string, len(args.))
	for name, device := range spec.Devices {
		nic := make(map[string]string, len(device))
		for key, value := range device {
			nic[key] = value
		}
		nics[name] = nic
	}
*/
	spec := api.ContainersPost{
		Name: hostname,
		ContainerPut: api.ContainerPut{
			Profiles: profiles,
			Devices:   make(map[string]map[string]string),
			Config:    metadata,
		},
	}
	op, err := env.client.CreateContainerFromImage(imageServer, *image, spec)
	if err != nil {
		return nil, errors.Trace(err)
	}
	progress := func(op api.Operation) {
		if op.Metadata == nil {
			return
		}
		for _, key := range []string{"fs_progress", "download_progress"} {
			value, ok := op.Metadata[key]
			if ok {
				statusCallback(status.Provisioning, fmt.Sprintf("Retrieving image: %s", value.(string)))
				return
			}
		}
	}
	_, err = op.AddHandler(progress)
	if err != nil {
		return "", errors.Trace(err)
	}
	op.Wait()
	opInfo, err := op.GetTarget()
	if err != nil {
		return "", errors.Trace(err)
	}
	if opInfo.StatusCode != api.Success {
		return "", fmt.Errorf("LXD error: %s", opInfo.Err)
	}
	statusCallback(status.Running, "container started")
	return , nil
}

// getMetadata builds the raw "user-defined" metadata for the new
// instance (relative to the provided args) and returns it.
func getMetadata(cloudcfg cloudinit.CloudConfig, args environs.StartInstanceParams) (map[string]string, error) {
	renderer := lxdRenderer{}
	uncompressed, err := providerinit.ComposeUserData(args.InstanceConfig, cloudcfg, renderer)
	if err != nil {
		return nil, errors.Annotate(err, "cannot make user data")
	}
	logger.Debugf("LXD user data; %d bytes", len(uncompressed))

	// TODO(ericsnow) Looks like LXD does not handle gzipped userdata
	// correctly.  It likely has to do with the HTTP transport, much
	// as we have to b64encode the userdata for GCE.  Until that is
	// resolved we simply pass the plain text.
	//compressed := utils.Gzip(compressed)
	userdata := string(uncompressed)

	metadata := map[string]string{
		// store the cloud-config userdata for cloud-init.
		metadataKeyCloudInit: userdata,
	}
	for k, v := range args.InstanceConfig.Tags {
		if !strings.HasPrefix(k, tags.JujuTagPrefix) {
			// Since some metadata is interpreted by LXD,
			// we cannot allow arbitrary tags to be passed
			// in by the user. We currently only pass through
			// Juju-defined tags.
			//
			// TODO(axw) 2016-04-11 #1568666
			// We should reject non-juju tags in config validation.
			logger.Debugf("ignoring non-juju tag: %s=%s", k, v)
			continue
		}
		metadata[k] = v
	}

	return metadata, nil
}

// getHardwareCharacteristics compiles hardware-related details about
// the given instance and relative to the provided spec and returns it.
func (env *environ) getHardwareCharacteristics(args environs.StartInstanceParams, inst *environInstance) *instance.HardwareCharacteristics {
	raw := inst.raw.Hardware

	archStr := raw.Architecture
	if archStr == "unknown" || !arch.IsSupportedArch(archStr) {
		// TODO(ericsnow) This special-case should be improved.
		archStr = arch.HostArch()
	}
	cores := uint64(raw.NumCores)
	mem := uint64(raw.MemoryMB)
	return &instance.HardwareCharacteristics{
		Arch:     &archStr,
		CpuCores: &cores,
		Mem:      &mem,
	}
}

// AllInstances implements environs.InstanceBroker.
func (env *environ) AllInstances() ([]instance.Instance, error) {
	environInstances, err := env.allInstances()
	instances := make([]instance.Instance, len(environInstances))
	for i, inst := range environInstances {
		if inst == nil {
			continue
		}
		instances[i] = inst
	}
	return instances, err
}

// StopInstances implements environs.InstanceBroker.
func (env *environ) StopInstances(instances ...instance.Id) error {
	var ids []string
	for _, id := range instances {
		ids = append(ids, string(id))
	}
	prefix := env.namespace.Prefix()
	err := env.raw.RemoveInstances(prefix, ids...)
	return errors.Trace(err)
}

// Copyright 2017 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package lxd

import (
	"github.com/juju/loggo"

	"github.com/lxc/lxd/client"
	
	"github.com/juju/juju/environs"
)

var (
	logger = loggo.GetLogger("juju.provider.lxd")
)

func connectLXD(spec environs.CloudSpec, local bool) (lxd.ContainerServer, error) {
	if local {
		return connectLocalLXD()
	}
	return connectRemoteLXD(spec)
}

func connectLocalLXD() (lxd.ContainerServer, error) {
	client, err := lxd.ConnectLXDUnix(lxdSocketPath(), &lxd.ConnectionArgs{})

	if err != nil {
		return nil, errors.Trace(err)
	}

	return client, nil
}

func connectRemoteLXD(spec environs.CloudSpec) (lxd.ContainerServer, error) {
	hostname, args, err := getRemoteConfig(spec)
	if err != nil {
		return nil, errors.Trace(err)
	}
	return lxd.ConnectLXD(hostname, args)
}

// getRemoteConfig returns lxd.ConnectionArgs using a TCP-based remote.
func getRemoteConfig(spec environs.CloudSpec) (string, *lxd.ConnectionArgs, error) {
	var args lxd.ConnectionArgs
	var ok bool
	if spec.Credential == nil {
		return "", errors.NotValidf("credentials")
	}
	credAttrs := spec.Credential.Attributes()
	args.TLSClientCert, ok = credAttrs[credAttrClientCert]
	if !ok {
		return "", errors.NotValidf("credentials")
	}
	args.TLSClientKey, ok = credAttrs[credAttrClientKey]
	if !ok {
		return "", errors.NotValidf("credentials")
	}
	args.TLSServerCert, ok = credAttrs[credAttrServerCert]
	if !ok {
		return "", errors.NotValidf("credentials")
	}
	return spec.Endpoint, &args, nil
}

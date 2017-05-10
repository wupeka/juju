// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package discoverspaces

import (
	"github.com/juju/errors"

	"github.com/juju/juju/api/base"
	apiwatcher "github.com/juju/juju/api/watcher"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/environs/config"
	"github.com/juju/juju/watcher"
)

const discoverspacesFacade = "DiscoverSpaces"

// API provides access to the DiscoverSpaces API facade.
type API struct {
	facade base.FacadeCaller
}

// NewAPI creates a new facade.
func NewAPI(caller base.APICaller) *API {
	if caller == nil {
		panic("caller is nil")
	}
	facadeCaller := base.NewFacadeCaller(caller, discoverspacesFacade)
	return &API{
		facade: facadeCaller,
	}
}

func (api *API) ListSpaces() (params.DiscoverSpacesResults, error) {
	var result params.DiscoverSpacesResults
	if err := api.facade.FacadeCall("ListSpaces", nil, &result); err != nil {
		return result, errors.Trace(err)
	}
	return result, nil
}

func (api *API) AddSubnets(args params.AddSubnetsParams) (params.ErrorResults, error) {
	var result params.ErrorResults
	err := api.facade.FacadeCall("AddSubnets", args, &result)
	if err != nil {
		return result, errors.Trace(err)
	}
	return result, nil
}

func (api *API) CreateSpaces(args params.CreateSpacesParams) (results params.ErrorResults, err error) {
	var result params.ErrorResults
	err = api.facade.FacadeCall("CreateSpaces", args, &result)
	if err != nil {
		return result, errors.Trace(err)
	}
	return result, nil
}

func (api *API) ListSubnets(args params.SubnetsFilters) (params.ListSubnetsResults, error) {
	var result params.ListSubnetsResults
	if err := api.facade.FacadeCall("ListSubnets", args, &result); err != nil {
		return result, errors.Trace(err)
	}
	return result, nil
}

// ModelConfig returns the current model configuration.
func (api *API) ModelConfig() (*config.Config, error) {
	var result params.ModelConfigResult
	err := api.facade.FacadeCall("ModelConfig", nil, &result)
	if err != nil {
		return nil, err
	}
	conf, err := config.New(config.NoDefaults, result.Config)
	if err != nil {
		return nil, err
	}
	return conf, nil
}

// WatchSpacesSyncSettings watches for requests to resync spaces with provider
func (api *API) WatchSpacesSyncSettings() (watcher.NotifyWatcher, error) {
	var result params.NotifyWatchResult
	err := api.facade.FacadeCall("WatchSpacesSyncSettings", nil, &result)
	if err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, result.Error
	}

	return newNotifyWatcher(api.facade.RawAPICaller(), result), nil
}

var newNotifyWatcher = apiwatcher.NewNotifyWatcher

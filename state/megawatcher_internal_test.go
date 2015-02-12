// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/juju/names"
	gitjujutesting "github.com/juju/testing"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/charm.v4"

	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/constraints"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/network"
	"github.com/juju/juju/state/multiwatcher"
	"github.com/juju/juju/state/watcher"
	"github.com/juju/juju/testing"
)

var (
	_ backingEntityDoc = (*backingMachine)(nil)
	_ backingEntityDoc = (*backingUnit)(nil)
	_ backingEntityDoc = (*backingService)(nil)
	_ backingEntityDoc = (*backingRelation)(nil)
	_ backingEntityDoc = (*backingAnnotation)(nil)
	_ backingEntityDoc = (*backingStatus)(nil)
	_ backingEntityDoc = (*backingConstraints)(nil)
	_ backingEntityDoc = (*backingSettings)(nil)
	_ backingEntityDoc = (*backingOpenedPorts)(nil)
)

var dottedConfig = `
options:
  key.dotted: {default: My Key, description: Desc, type: string}
`

type storeManagerStateSuite struct {
	testing.BaseSuite
	gitjujutesting.MgoSuite
	State *State
	owner names.UserTag
}

func (s *storeManagerStateSuite) SetUpSuite(c *gc.C) {
	s.BaseSuite.SetUpSuite(c)
	s.MgoSuite.SetUpSuite(c)
}

func (s *storeManagerStateSuite) TearDownSuite(c *gc.C) {
	s.MgoSuite.TearDownSuite(c)
	s.BaseSuite.TearDownSuite(c)
}

func (s *storeManagerStateSuite) SetUpTest(c *gc.C) {
	s.BaseSuite.SetUpTest(c)
	s.MgoSuite.SetUpTest(c)
	s.owner = names.NewLocalUserTag("test-admin")
	st, err := Initialize(s.owner, TestingMongoInfo(), testing.EnvironConfig(c), TestingDialOpts(), nil)
	c.Assert(err, gc.IsNil)
	s.State = st
}

func (s *storeManagerStateSuite) TearDownTest(c *gc.C) {
	if s.State != nil {
		s.State.Close()
	}
	s.MgoSuite.TearDownTest(c)
	s.BaseSuite.TearDownTest(c)
}

func (s *storeManagerStateSuite) Reset(c *gc.C) {
	s.TearDownTest(c)
	s.SetUpTest(c)
}

var _ = gc.Suite(&storeManagerStateSuite{})

// setUpScenario adds some entities to the state so that
// we can check that they all get pulled in by
// allWatcherStateBacking.getAll.
func (s *storeManagerStateSuite) setUpScenario(c *gc.C) (entities entityInfoSlice) {
	add := func(e multiwatcher.EntityInfo) {
		entities = append(entities, e)
	}
	m, err := s.State.AddMachine("quantal", JobManageEnviron)
	c.Assert(err, gc.IsNil)
	c.Assert(m.Tag(), gc.Equals, names.NewMachineTag("0"))
	// TODO(dfc) instance.Id should take a TAG!
	err = m.SetProvisioned(instance.Id("i-"+m.Tag().String()), "fake_nonce", nil)
	c.Assert(err, gc.IsNil)
	hc, err := m.HardwareCharacteristics()
	c.Assert(err, gc.IsNil)
	err = m.SetAddresses(network.NewAddress("example.com", network.ScopeUnknown))
	c.Assert(err, gc.IsNil)
	add(&params.MachineInfo{
		Id:                      "0",
		InstanceId:              "i-machine-0",
		Status:                  params.StatusPending,
		Life:                    params.Alive,
		Series:                  "quantal",
		Jobs:                    []params.MachineJob{JobManageEnviron.ToParams()},
		Addresses:               m.Addresses(),
		HardwareCharacteristics: hc,
	})

	wordpress := AddTestingService(c, s.State, "wordpress", AddTestingCharm(c, s.State, "wordpress"), s.owner)
	err = wordpress.SetExposed()
	c.Assert(err, gc.IsNil)
	err = wordpress.SetMinUnits(3)
	c.Assert(err, gc.IsNil)
	err = wordpress.SetConstraints(constraints.MustParse("mem=100M"))
	c.Assert(err, gc.IsNil)
	setServiceConfigAttr(c, wordpress, "blog-title", "boring")
	add(&params.ServiceInfo{
		Name:        "wordpress",
		Exposed:     true,
		CharmURL:    serviceCharmURL(wordpress).String(),
		OwnerTag:    s.owner.String(),
		Life:        params.Alive,
		MinUnits:    3,
		Constraints: constraints.MustParse("mem=100M"),
		Config:      charm.Settings{"blog-title": "boring"},
		Subordinate: false,
	})
	pairs := map[string]string{"x": "12", "y": "99"}
	err = wordpress.SetAnnotations(pairs)
	c.Assert(err, gc.IsNil)
	add(&params.AnnotationInfo{
		Tag:         "service-wordpress",
		Annotations: pairs,
	})

	logging := AddTestingService(c, s.State, "logging", AddTestingCharm(c, s.State, "logging"), s.owner)
	add(&params.ServiceInfo{
		Name:        "logging",
		CharmURL:    serviceCharmURL(logging).String(),
		OwnerTag:    s.owner.String(),
		Life:        params.Alive,
		Config:      charm.Settings{},
		Subordinate: true,
	})

	eps, err := s.State.InferEndpoints("logging", "wordpress")
	c.Assert(err, gc.IsNil)
	rel, err := s.State.AddRelation(eps...)
	c.Assert(err, gc.IsNil)
	add(&params.RelationInfo{
		Key: "logging:logging-directory wordpress:logging-dir",
		Id:  rel.Id(),
		Endpoints: []params.Endpoint{
			{ServiceName: "logging", Relation: charm.Relation{Name: "logging-directory", Role: "requirer", Interface: "logging", Optional: false, Limit: 1, Scope: "container"}},
			{ServiceName: "wordpress", Relation: charm.Relation{Name: "logging-dir", Role: "provider", Interface: "logging", Optional: false, Limit: 0, Scope: "container"}}},
	})

	for i := 0; i < 2; i++ {
		wu, err := wordpress.AddUnit()
		c.Assert(err, gc.IsNil)
		c.Assert(wu.Tag().String(), gc.Equals, fmt.Sprintf("unit-wordpress-%d", i))

		m, err := s.State.AddMachine("quantal", JobHostUnits)
		c.Assert(err, gc.IsNil)
		c.Assert(m.Tag().String(), gc.Equals, fmt.Sprintf("machine-%d", i+1))

		add(&params.UnitInfo{
			Name:        fmt.Sprintf("wordpress/%d", i),
			Service:     wordpress.Name(),
			Series:      m.Series(),
			MachineId:   m.Id(),
			Status:      params.StatusPending,
			Subordinate: false,
		})
		pairs := map[string]string{"name": fmt.Sprintf("bar %d", i)}
		err = wu.SetAnnotations(pairs)
		c.Assert(err, gc.IsNil)
		add(&params.AnnotationInfo{
			Tag:         fmt.Sprintf("unit-wordpress-%d", i),
			Annotations: pairs,
		})

		err = m.SetProvisioned(instance.Id("i-"+m.Tag().String()), "fake_nonce", nil)
		c.Assert(err, gc.IsNil)
		err = m.SetStatus(StatusError, m.Tag().String(), nil)
		c.Assert(err, gc.IsNil)
		hc, err := m.HardwareCharacteristics()
		c.Assert(err, gc.IsNil)
		add(&params.MachineInfo{
			Id:                      fmt.Sprint(i + 1),
			InstanceId:              "i-" + m.Tag().String(),
			Status:                  params.StatusError,
			StatusInfo:              m.Tag().String(),
			Life:                    params.Alive,
			Series:                  "quantal",
			Jobs:                    []params.MachineJob{JobHostUnits.ToParams()},
			Addresses:               []network.Address{},
			HardwareCharacteristics: hc,
		})
		err = wu.AssignToMachine(m)
		c.Assert(err, gc.IsNil)

		deployer, ok := wu.DeployerTag()
		c.Assert(ok, gc.Equals, true)
		c.Assert(deployer, gc.Equals, names.NewMachineTag(fmt.Sprintf("%d", i+1)))

		wru, err := rel.Unit(wu)
		c.Assert(err, gc.IsNil)

		// Create the subordinate unit as a side-effect of entering
		// scope in the principal's relation-unit.
		err = wru.EnterScope(nil)
		c.Assert(err, gc.IsNil)

		lu, err := s.State.Unit(fmt.Sprintf("logging/%d", i))
		c.Assert(err, gc.IsNil)
		c.Assert(lu.IsPrincipal(), gc.Equals, false)
		deployer, ok = lu.DeployerTag()
		c.Assert(ok, gc.Equals, true)
		c.Assert(deployer, gc.Equals, names.NewUnitTag(fmt.Sprintf("wordpress/%d", i)))
		add(&params.UnitInfo{
			Name:        fmt.Sprintf("logging/%d", i),
			Service:     "logging",
			Series:      "quantal",
			Status:      params.StatusPending,
			Subordinate: true,
		})
	}
	return
}

func serviceCharmURL(svc *Service) *charm.URL {
	url, _ := svc.CharmURL()
	return url
}

func jcDeepEqualsCheck(c *gc.C, got, want interface{}) bool {
	ok, message := jc.DeepEquals.Check([]interface{}{got, want}, []string{"got", "want"})
	if !ok {
		c.Logf(message)
	}
	return ok
}

func assertEntitiesEqual(c *gc.C, got, want []multiwatcher.EntityInfo) {
	if len(got) == 0 {
		got = nil
	}
	if len(want) == 0 {
		want = nil
	}
	if jcDeepEqualsCheck(c, got, want) {
		return
	}
	c.Errorf("entity mismatch; got len %d; want %d", len(got), len(want))
	c.Logf("got:")
	for _, e := range got {
		c.Logf("\t%T %#v", e, e)
	}
	c.Logf("expected:")
	for _, e := range want {
		c.Logf("\t%T %#v", e, e)
	}
	c.FailNow()
}

func (s *storeManagerStateSuite) TestStateBackingGetAll(c *gc.C) {
	expectEntities := s.setUpScenario(c)
	b := newAllWatcherStateBacking(s.State)
	all := multiwatcher.NewStore()
	err := b.GetAll(all)
	c.Assert(err, gc.IsNil)
	var gotEntities entityInfoSlice = all.All()
	sort.Sort(gotEntities)
	sort.Sort(expectEntities)
	assertEntitiesEqual(c, gotEntities, expectEntities)
}

func setServiceConfigAttr(c *gc.C, svc *Service, attr string, val interface{}) {
	err := svc.UpdateConfigSettings(charm.Settings{attr: val})
	c.Assert(err, gc.IsNil)
}

func (s *storeManagerStateSuite) TestChanged(c *gc.C) {
	for i, test := range []struct {
		about          string
		add            []multiwatcher.EntityInfo
		setUp          func(c *gc.C, st *State)
		change         func(st *State) watcher.Change
		expectContents []multiwatcher.EntityInfo
	}{
		// Machine changes
		{
			about: "no machine in state, no machine in store -> do nothing",
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "machines",
					Id: st.docID("1"),
				}
			},
		}, {
			about: "machine is removed if it's not in backing",
			add:   []multiwatcher.EntityInfo{&params.MachineInfo{Id: "1"}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "machines",
					Id: st.docID("1"),
				}
			},
		}, {
			about: "machine is added if it's in backing but not in Store",
			setUp: func(c *gc.C, st *State) {
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, gc.IsNil)
				err = m.SetStatus(StatusError, "failure", nil)
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "machines",
					Id: st.docID("0"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.MachineInfo{
					Id:         "0",
					Status:     params.StatusError,
					StatusInfo: "failure",
					Life:       params.Alive,
					Series:     "quantal",
					Jobs:       []params.MachineJob{JobHostUnits.ToParams()},
					Addresses:  []network.Address{},
				},
			},
		},
		// Machine status changes
		{
			about: "machine is updated if it's in backing and in Store",
			add: []multiwatcher.EntityInfo{
				&params.MachineInfo{
					Id:         "0",
					Status:     params.StatusError,
					StatusInfo: "another failure",
				},
			},
			setUp: func(c *gc.C, st *State) {
				m, err := st.AddMachine("trusty", JobManageEnviron)
				c.Assert(err, gc.IsNil)
				err = m.SetProvisioned("i-0", "bootstrap_nonce", nil)
				c.Assert(err, gc.IsNil)
				err = m.SetSupportedContainers([]instance.ContainerType{instance.LXC})
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "machines",
					Id: st.docID("0"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.MachineInfo{
					Id:                       "0",
					InstanceId:               "i-0",
					Status:                   params.StatusError,
					StatusInfo:               "another failure",
					Life:                     params.Alive,
					Series:                   "trusty",
					Jobs:                     []params.MachineJob{JobManageEnviron.ToParams()},
					Addresses:                []network.Address{},
					HardwareCharacteristics:  &instance.HardwareCharacteristics{},
					SupportedContainers:      []instance.ContainerType{instance.LXC},
					SupportedContainersKnown: true,
				},
			},
		},
		// Unit changes
		{
			about: "no unit in state, no unit in store -> do nothing",
			setUp: func(c *gc.C, st *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "units",
					Id: st.docID("1"),
				}
			},
		}, {
			about: "unit is removed if it's not in backing",
			add:   []multiwatcher.EntityInfo{&params.UnitInfo{Name: "wordpress/1"}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "units",
					Id: st.docID("wordpress/1"),
				}
			},
		}, {
			about: "unit is added if it's in backing but not in Store",
			setUp: func(c *gc.C, st *State) {
				wordpress := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				u, err := wordpress.AddUnit()
				c.Assert(err, gc.IsNil)
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, gc.IsNil)
				err = u.AssignToMachine(m)
				c.Assert(err, gc.IsNil)
				// Open two ports and one range.
				err = u.OpenPort("tcp", 12345)
				c.Assert(err, gc.IsNil)
				err = u.OpenPort("udp", 54321)
				c.Assert(err, jc.ErrorIsNil)
				err = u.OpenPorts("tcp", 5555, 5558)
				c.Assert(err, jc.ErrorIsNil)
				err = u.SetStatus(StatusError, "failure", nil)
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "units",
					Id: st.docID("wordpress/0"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name:      "wordpress/0",
					Service:   "wordpress",
					Series:    "quantal",
					MachineId: "0",
					Ports: []network.Port{
						{"tcp", 5555},
						{"tcp", 5556},
						{"tcp", 5557},
						{"tcp", 5558},
						{"tcp", 12345},
						{"udp", 54321},
					},
					PortRanges: []network.PortRange{
						{5555, 5558, "tcp"},
						{12345, 12345, "tcp"},
						{54321, 54321, "udp"},
					},
					Status:     params.StatusError,
					StatusInfo: "failure",
				},
			},
		}, {
			about: "unit is updated if it's in backing and in multiwatcher.Store",
			add: []multiwatcher.EntityInfo{&params.UnitInfo{
				Name:       "wordpress/0",
				Status:     params.StatusError,
				StatusInfo: "another failure",
				Ports:      []network.Port{{"udp", 17070}},
				PortRanges: []network.PortRange{{17070, 17070, "udp"}},
			}},
			setUp: func(c *gc.C, st *State) {
				wordpress := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				u, err := wordpress.AddUnit()
				c.Assert(err, gc.IsNil)
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, gc.IsNil)
				err = u.AssignToMachine(m)
				c.Assert(err, gc.IsNil)
				err = u.OpenPort("udp", 17070)
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "units",
					Id: st.docID("wordpress/0"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name:       "wordpress/0",
					Service:    "wordpress",
					Series:     "quantal",
					MachineId:  "0",
					Ports:      []network.Port{{"udp", 17070}},
					PortRanges: []network.PortRange{{17070, 17070, "udp"}},
					Status:     params.StatusError,
					StatusInfo: "another failure",
				},
			},
		}, {
			about: "unit info is updated if a port is opened on the machine it is placed in",
			add: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name: "wordpress/0",
				},
				&params.MachineInfo{
					Id: "0",
				},
			},
			setUp: func(c *gc.C, st *State) {
				wordpress := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				u, err := wordpress.AddUnit()
				c.Assert(err, jc.ErrorIsNil)
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, jc.ErrorIsNil)
				err = u.AssignToMachine(m)
				c.Assert(err, jc.ErrorIsNil)
				err = u.OpenPort("tcp", 4242)
				c.Assert(err, jc.ErrorIsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  openedPortsC,
					Id: "m#0#n#juju-public",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name:       "wordpress/0",
					Ports:      []network.Port{{"tcp", 4242}},
					PortRanges: []network.PortRange{{4242, 4242, "tcp"}},
				},
				&params.MachineInfo{
					Id: "0",
				},
			},
		}, {
			about: "unit is created if a port is opened on the machine it is placed in",
			add: []multiwatcher.EntityInfo{
				&params.MachineInfo{
					Id: "0",
				},
			},
			setUp: func(c *gc.C, st *State) {
				wordpress := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				u, err := wordpress.AddUnit()
				c.Assert(err, jc.ErrorIsNil)
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, jc.ErrorIsNil)
				err = u.AssignToMachine(m)
				c.Assert(err, jc.ErrorIsNil)
				err = u.OpenPorts("tcp", 21, 22)
				c.Assert(err, jc.ErrorIsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "units",
					Id: st.docID("wordpress/0"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name:       "wordpress/0",
					Service:    "wordpress",
					Series:     "quantal",
					MachineId:  "0",
					Status:     "pending",
					Ports:      []network.Port{{"tcp", 21}, {"tcp", 22}},
					PortRanges: []network.PortRange{{21, 22, "tcp"}},
				},
				&params.MachineInfo{
					Id: "0",
				},
			},
		}, {
			about: "unit addresses are read from the assigned machine for recent Juju releases",
			setUp: func(c *gc.C, st *State) {
				wordpress := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				u, err := wordpress.AddUnit()
				c.Assert(err, gc.IsNil)
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, gc.IsNil)
				err = u.AssignToMachine(m)
				c.Assert(err, gc.IsNil)
				err = u.OpenPort("tcp", 12345)
				c.Assert(err, gc.IsNil)
				publicAddress := network.NewAddress("public", network.ScopePublic)
				privateAddress := network.NewAddress("private", network.ScopeCloudLocal)
				err = m.SetAddresses(publicAddress, privateAddress)
				c.Assert(err, gc.IsNil)
				err = u.SetStatus(StatusError, "failure", nil)
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "units",
					Id: st.docID("wordpress/0"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name:           "wordpress/0",
					Service:        "wordpress",
					Series:         "quantal",
					PublicAddress:  "public",
					PrivateAddress: "private",
					MachineId:      "0",
					Ports:          []network.Port{{"tcp", 12345}},
					PortRanges:     []network.PortRange{{12345, 12345, "tcp"}},
					Status:         params.StatusError,
					StatusInfo:     "failure",
				},
			},
		},
		// Service changes
		{
			about: "no service in state, no service in store -> do nothing",
			setUp: func(c *gc.C, st *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "services",
					Id: st.docID("wordpress"),
				}
			},
		}, {
			about: "service is removed if it's not in backing",
			add:   []multiwatcher.EntityInfo{&params.ServiceInfo{Name: "wordpress"}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "services",
					Id: st.docID("wordpress"),
				}
			},
		}, {
			about: "service is added if it's in backing but not in Store",
			setUp: func(c *gc.C, st *State) {
				wordpress := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				err := wordpress.SetExposed()
				c.Assert(err, gc.IsNil)
				err = wordpress.SetMinUnits(42)
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "services",
					Id: st.docID("wordpress"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.ServiceInfo{
					Name:     "wordpress",
					Exposed:  true,
					CharmURL: "local:quantal/quantal-wordpress-3",
					OwnerTag: s.owner.String(),
					Life:     params.Alive,
					MinUnits: 42,
					Config:   charm.Settings{},
				},
			},
		}, {
			about: "service is updated if it's in backing and in multiwatcher.Store",
			add: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:        "wordpress",
				Exposed:     true,
				CharmURL:    "local:quantal/quantal-wordpress-3",
				MinUnits:    47,
				Constraints: constraints.MustParse("mem=99M"),
				Config:      charm.Settings{"blog-title": "boring"},
			}},
			setUp: func(c *gc.C, st *State) {
				svc := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				setServiceConfigAttr(c, svc, "blog-title", "boring")
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "services",
					Id: st.docID("wordpress"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.ServiceInfo{
					Name:        "wordpress",
					CharmURL:    "local:quantal/quantal-wordpress-3",
					OwnerTag:    s.owner.String(),
					Life:        params.Alive,
					Constraints: constraints.MustParse("mem=99M"),
					Config:      charm.Settings{"blog-title": "boring"},
				},
			},
		}, {
			about: "service re-reads config when charm URL changes",
			add: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name: "wordpress",
				// Note: CharmURL has a different revision number from
				// the wordpress revision in the testing repo.
				CharmURL: "local:quantal/quantal-wordpress-2",
				Config:   charm.Settings{"foo": "bar"},
			}},
			setUp: func(c *gc.C, st *State) {
				svc := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				setServiceConfigAttr(c, svc, "blog-title", "boring")
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "services",
					Id: st.docID("wordpress"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.ServiceInfo{
					Name:     "wordpress",
					CharmURL: "local:quantal/quantal-wordpress-3",
					OwnerTag: s.owner.String(),
					Life:     params.Alive,
					Config:   charm.Settings{"blog-title": "boring"},
				},
			},
		},
		// Relation changes
		{
			about: "no relation in state, no service in store -> do nothing",
			setUp: func(c *gc.C, st *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "relations",
					Id: st.docID("logging:logging-directory wordpress:logging-dir"),
				}
			},
		}, {
			about: "relation is removed if it's not in backing",
			add:   []multiwatcher.EntityInfo{&params.RelationInfo{Key: "logging:logging-directory wordpress:logging-dir"}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "relations",
					Id: st.docID("logging:logging-directory wordpress:logging-dir"),
				}
			},
		}, {
			about: "relation is added if it's in backing but not in Store",
			setUp: func(c *gc.C, st *State) {
				AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)

				AddTestingService(c, st, "logging", AddTestingCharm(c, st, "logging"), s.owner)
				eps, err := st.InferEndpoints("logging", "wordpress")
				c.Assert(err, gc.IsNil)
				_, err = st.AddRelation(eps...)
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "relations",
					Id: st.docID("logging:logging-directory wordpress:logging-dir"),
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.RelationInfo{
					Key: "logging:logging-directory wordpress:logging-dir",
					Endpoints: []params.Endpoint{
						{ServiceName: "logging", Relation: charm.Relation{Name: "logging-directory", Role: "requirer", Interface: "logging", Optional: false, Limit: 1, Scope: "container"}},
						{ServiceName: "wordpress", Relation: charm.Relation{Name: "logging-dir", Role: "provider", Interface: "logging", Optional: false, Limit: 0, Scope: "container"}}},
				},
			},
		},
		// Annotation changes
		{
			about: "no annotation in state, no annotation in store -> do nothing",
			setUp: func(c *gc.C, st *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "annotations",
					Id: "m#0",
				}
			},
		}, {
			about: "annotation is removed if it's not in backing",
			add:   []multiwatcher.EntityInfo{&params.AnnotationInfo{Tag: "machine-0"}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "annotations",
					Id: "m#0",
				}
			},
		}, {
			about: "annotation is added if it's in backing but not in Store",
			setUp: func(c *gc.C, st *State) {
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, gc.IsNil)
				err = m.SetAnnotations(map[string]string{"foo": "bar", "arble": "baz"})
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "annotations",
					Id: "m#0",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.AnnotationInfo{
					Tag:         "machine-0",
					Annotations: map[string]string{"foo": "bar", "arble": "baz"},
				},
			},
		}, {
			about: "annotation is updated if it's in backing and in multiwatcher.Store",
			add: []multiwatcher.EntityInfo{&params.AnnotationInfo{
				Tag: "machine-0",
				Annotations: map[string]string{
					"arble":  "baz",
					"foo":    "bar",
					"pretty": "polly",
				},
			}},
			setUp: func(c *gc.C, st *State) {
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, gc.IsNil)
				err = m.SetAnnotations(map[string]string{
					"arble":  "khroomph",
					"pretty": "",
					"new":    "attr",
				})
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "annotations",
					Id: "m#0",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.AnnotationInfo{
					Tag: "machine-0",
					Annotations: map[string]string{
						"arble": "khroomph",
						"new":   "attr",
					},
				},
			},
		},
		// Unit status changes
		{
			about: "no unit in state -> do nothing",
			setUp: func(c *gc.C, st *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "statuses",
					Id: "u#wordpress/0",
				}
			},
		}, {
			about: "no change if status is not in backing",
			add: []multiwatcher.EntityInfo{&params.UnitInfo{
				Name:       "wordpress/0",
				Status:     params.StatusError,
				StatusInfo: "failure",
			}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "statuses",
					Id: "u#wordpress/0",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name:       "wordpress/0",
					Status:     params.StatusError,
					StatusInfo: "failure",
				},
			},
		}, {
			about: "status is changed if the unit exists in the store",
			add: []multiwatcher.EntityInfo{&params.UnitInfo{
				Name:       "wordpress/0",
				Status:     params.StatusError,
				StatusInfo: "failure",
			}},
			setUp: func(c *gc.C, st *State) {
				wordpress := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				u, err := wordpress.AddUnit()
				c.Assert(err, gc.IsNil)
				err = u.SetStatus(StatusStarted, "", nil)
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "statuses",
					Id: "u#wordpress/0",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name:       "wordpress/0",
					Status:     params.StatusStarted,
					StatusData: make(map[string]interface{}),
				},
			},
		}, {
			about: "status is changed with additional status data",
			add: []multiwatcher.EntityInfo{&params.UnitInfo{
				Name:   "wordpress/0",
				Status: params.StatusStarted,
			}},
			setUp: func(c *gc.C, st *State) {
				wordpress := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				u, err := wordpress.AddUnit()
				c.Assert(err, gc.IsNil)
				err = u.SetStatus(StatusError, "hook error", map[string]interface{}{
					"1st-key": "one",
					"2nd-key": 2,
					"3rd-key": true,
				})
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "statuses",
					Id: "u#wordpress/0",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.UnitInfo{
					Name:       "wordpress/0",
					Status:     params.StatusError,
					StatusInfo: "hook error",
					StatusData: map[string]interface{}{
						"1st-key": "one",
						"2nd-key": 2,
						"3rd-key": true,
					},
				},
			},
		},
		// Machine status changes
		{
			about: "no machine in state -> do nothing",
			setUp: func(c *gc.C, st *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "statuses",
					Id: "m#0",
				}
			},
		}, {
			about: "no change if status is not in backing",
			add: []multiwatcher.EntityInfo{&params.MachineInfo{
				Id:         "0",
				Status:     params.StatusError,
				StatusInfo: "failure",
			}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "statuses",
					Id: "m#0",
				}
			},
			expectContents: []multiwatcher.EntityInfo{&params.MachineInfo{
				Id:         "0",
				Status:     params.StatusError,
				StatusInfo: "failure",
			}},
		}, {
			about: "status is changed if the machine exists in the store",
			add: []multiwatcher.EntityInfo{&params.MachineInfo{
				Id:         "0",
				Status:     params.StatusError,
				StatusInfo: "failure",
			}},
			setUp: func(c *gc.C, st *State) {
				m, err := st.AddMachine("quantal", JobHostUnits)
				c.Assert(err, gc.IsNil)
				err = m.SetStatus(StatusStarted, "", nil)
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "statuses",
					Id: "m#0",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.MachineInfo{
					Id:         "0",
					Status:     params.StatusStarted,
					StatusData: make(map[string]interface{}),
				},
			},
		},
		// Service constraints changes
		{
			about: "no service in state -> do nothing",
			setUp: func(c *gc.C, st *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "constraints",
					Id: "s#wordpress",
				}
			},
		}, {
			about: "no change if service is not in backing",
			add: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:        "wordpress",
				Constraints: constraints.MustParse("mem=99M"),
			}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "constraints",
					Id: "s#wordpress",
				}
			},
			expectContents: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:        "wordpress",
				Constraints: constraints.MustParse("mem=99M"),
			}},
		}, {
			about: "status is changed if the service exists in the store",
			add: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:        "wordpress",
				Constraints: constraints.MustParse("mem=99M cpu-cores=2 cpu-power=4"),
			}},
			setUp: func(c *gc.C, st *State) {
				svc := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				err := svc.SetConstraints(constraints.MustParse("mem=4G cpu-cores= arch=amd64"))
				c.Assert(err, gc.IsNil)
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "constraints",
					Id: "s#wordpress",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.ServiceInfo{
					Name:        "wordpress",
					Constraints: constraints.MustParse("mem=4G cpu-cores= arch=amd64"),
				},
			},
		},
		// Service config changes.
		{
			about: "no service in state -> do nothing",
			setUp: func(c *gc.C, st *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "settings",
					Id: "s#wordpress#local:quantal/quantal-wordpress-3",
				}
			},
		}, {
			about: "no change if service is not in backing",
			add: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:     "wordpress",
				CharmURL: "local:quantal/quantal-wordpress-3",
			}},
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "settings",
					Id: "s#wordpress#local:quantal/quantal-wordpress-3",
				}
			},
			expectContents: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:     "wordpress",
				CharmURL: "local:quantal/quantal-wordpress-3",
			}},
		}, {
			about: "service config is changed if service exists in the store with the same URL",
			add: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:     "wordpress",
				CharmURL: "local:quantal/quantal-wordpress-3",
				Config:   charm.Settings{"foo": "bar"},
			}},
			setUp: func(c *gc.C, st *State) {
				svc := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				setServiceConfigAttr(c, svc, "blog-title", "foo")
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "settings",
					Id: "s#wordpress#local:quantal/quantal-wordpress-3",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.ServiceInfo{
					Name:     "wordpress",
					CharmURL: "local:quantal/quantal-wordpress-3",
					Config:   charm.Settings{"blog-title": "foo"},
				},
			},
		}, {
			about: "service config is unescaped when reading from the backing store",
			add: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:     "wordpress",
				CharmURL: "local:quantal/quantal-wordpress-3",
				Config:   charm.Settings{"key.dotted": "bar"},
			}},
			setUp: func(c *gc.C, st *State) {
				testCharm := AddCustomCharm(
					c, st, "wordpress",
					"config.yaml", dottedConfig,
					"quantal", 3)
				svc := AddTestingService(c, st, "wordpress", testCharm, s.owner)
				setServiceConfigAttr(c, svc, "key.dotted", "foo")
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "settings",
					Id: "s#wordpress#local:quantal/quantal-wordpress-3",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.ServiceInfo{
					Name:     "wordpress",
					CharmURL: "local:quantal/quantal-wordpress-3",
					Config:   charm.Settings{"key.dotted": "foo"},
				},
			},
		}, {
			about: "service config is unchanged if service exists in the store with a different URL",
			add: []multiwatcher.EntityInfo{&params.ServiceInfo{
				Name:     "wordpress",
				CharmURL: "local:quantal/quantal-wordpress-2", // Note different revno.
				Config:   charm.Settings{"foo": "bar"},
			}},
			setUp: func(c *gc.C, st *State) {
				svc := AddTestingService(c, st, "wordpress", AddTestingCharm(c, st, "wordpress"), s.owner)
				setServiceConfigAttr(c, svc, "blog-title", "foo")
			},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "settings",
					Id: "s#wordpress#local:quantal/quantal-wordpress-3",
				}
			},
			expectContents: []multiwatcher.EntityInfo{
				&params.ServiceInfo{
					Name:     "wordpress",
					CharmURL: "local:quantal/quantal-wordpress-2",
					Config:   charm.Settings{"foo": "bar"},
				},
			},
		}, {
			about: "non-service config change is ignored",
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "settings",
					Id: "m#0",
				}
			},
		}, {
			about: "service config change with no charm url is ignored",
			setUp: func(*gc.C, *State) {},
			change: func(st *State) watcher.Change {
				return watcher.Change{
					C:  "settings",
					Id: "s#foo",
				}
			},
		},
	} {
		c.Logf("test %d. %s", i, test.about)
		b := newAllWatcherStateBacking(s.State)
		all := multiwatcher.NewStore()
		for _, info := range test.add {
			all.Update(info)
		}
		test.setUp(c, s.State)
		c.Logf("done set up")
		change := test.change(s.State)
		col, closer := s.State.getCollection(change.C)
		closer()
		change.C = col.Name
		err := b.Changed(all, change)
		c.Assert(err, gc.IsNil)
		assertEntitiesEqual(c, all.All(), test.expectContents)
		s.Reset(c)
	}
}

// TestStateWatcher tests the integration of the state watcher
// with the state-based backing. Most of the logic is tested elsewhere -
// this just tests end-to-end.
func (s *storeManagerStateSuite) TestStateWatcher(c *gc.C) {
	m0, err := s.State.AddMachine("trusty", JobManageEnviron)
	c.Assert(err, gc.IsNil)
	c.Assert(m0.Id(), gc.Equals, "0")

	m1, err := s.State.AddMachine("saucy", JobHostUnits)
	c.Assert(err, gc.IsNil)
	c.Assert(m1.Id(), gc.Equals, "1")

	b := newAllWatcherStateBacking(s.State)
	aw := multiwatcher.NewStoreManager(b)
	defer aw.Stop()
	w := multiwatcher.NewWatcher(aw)
	s.State.StartSync()
	checkNext(c, w, b, []params.Delta{{
		Entity: &params.MachineInfo{
			Id:        "0",
			Status:    params.StatusPending,
			Life:      params.Alive,
			Series:    "trusty",
			Jobs:      []params.MachineJob{JobManageEnviron.ToParams()},
			Addresses: []network.Address{},
		},
	}, {
		Entity: &params.MachineInfo{
			Id:        "1",
			Status:    params.StatusPending,
			Life:      params.Alive,
			Series:    "saucy",
			Jobs:      []params.MachineJob{JobHostUnits.ToParams()},
			Addresses: []network.Address{},
		},
	}}, "")

	// Make some changes to the state.
	arch := "amd64"
	mem := uint64(4096)
	hc := &instance.HardwareCharacteristics{
		Arch: &arch,
		Mem:  &mem,
	}
	err = m0.SetProvisioned("i-0", "bootstrap_nonce", hc)
	c.Assert(err, gc.IsNil)

	err = m1.Destroy()
	c.Assert(err, gc.IsNil)
	err = m1.EnsureDead()
	c.Assert(err, gc.IsNil)
	err = m1.Remove()
	c.Assert(err, gc.IsNil)

	m2, err := s.State.AddMachine("quantal", JobHostUnits)
	c.Assert(err, gc.IsNil)
	c.Assert(m2.Id(), gc.Equals, "2")

	wordpress := AddTestingService(c, s.State, "wordpress", AddTestingCharm(c, s.State, "wordpress"), s.owner)
	wu, err := wordpress.AddUnit()
	c.Assert(err, gc.IsNil)
	err = wu.AssignToMachine(m2)
	c.Assert(err, gc.IsNil)

	s.State.StartSync()

	// Check that we see the changes happen within a
	// reasonable time.
	var deltas []params.Delta
	for {
		d, err := getNext(c, w, 1*time.Second)
		if err == errTimeout {
			break
		}
		c.Assert(err, gc.IsNil)
		deltas = append(deltas, d...)
	}
	checkDeltasEqual(c, b, deltas, []params.Delta{{
		Entity: &params.MachineInfo{
			Id:                      "0",
			InstanceId:              "i-0",
			Status:                  params.StatusPending,
			Life:                    params.Alive,
			Series:                  "trusty",
			Jobs:                    []params.MachineJob{JobManageEnviron.ToParams()},
			Addresses:               []network.Address{},
			HardwareCharacteristics: hc,
		},
	}, {
		Removed: true,
		Entity: &params.MachineInfo{
			Id:        "1",
			Status:    params.StatusPending,
			Life:      params.Alive,
			Series:    "saucy",
			Jobs:      []params.MachineJob{JobHostUnits.ToParams()},
			Addresses: []network.Address{},
		},
	}, {
		Entity: &params.MachineInfo{
			Id:        "2",
			Status:    params.StatusPending,
			Life:      params.Alive,
			Series:    "quantal",
			Jobs:      []params.MachineJob{JobHostUnits.ToParams()},
			Addresses: []network.Address{},
		},
	}, {
		Entity: &params.ServiceInfo{
			Name:     "wordpress",
			CharmURL: "local:quantal/quantal-wordpress-3",
			OwnerTag: s.owner.String(),
			Life:     "alive",
			Config:   make(map[string]interface{}),
		},
	}, {
		Entity: &params.UnitInfo{
			Name:      "wordpress/0",
			Service:   "wordpress",
			Series:    "quantal",
			MachineId: "2",
			Status:    "pending",
		},
	}})

	err = w.Stop()
	c.Assert(err, gc.IsNil)

	_, err = w.Next()
	c.Assert(err, gc.ErrorMatches, multiwatcher.ErrWatcherStopped.Error())
}

type entityInfoSlice []multiwatcher.EntityInfo

func (s entityInfoSlice) Len() int      { return len(s) }
func (s entityInfoSlice) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s entityInfoSlice) Less(i, j int) bool {
	id0, id1 := s[i].EntityId(), s[j].EntityId()
	if id0.Kind != id1.Kind {
		return id0.Kind < id1.Kind
	}
	switch id := id0.Id.(type) {
	case string:
		return id < id1.Id.(string)
	default:
	}
	panic("unexpected entity id type")
}

var errTimeout = errors.New("no change received in sufficient time")

func getNext(c *gc.C, w *multiwatcher.Watcher, timeout time.Duration) ([]params.Delta, error) {
	var deltas []params.Delta
	var err error
	ch := make(chan struct{}, 1)
	go func() {
		deltas, err = w.Next()
		ch <- struct{}{}
	}()
	select {
	case <-ch:
		return deltas, err
	case <-time.After(timeout):
	}
	return nil, errTimeout
}

func checkNext(c *gc.C, w *multiwatcher.Watcher, b multiwatcher.Backing, deltas []params.Delta, expectErr string) {
	d, err := getNext(c, w, 1*time.Second)
	if expectErr != "" {
		c.Check(err, gc.ErrorMatches, expectErr)
		return
	}
	checkDeltasEqual(c, b, d, deltas)
}

// deltas are returns in arbitrary order, so we compare
// them as sets.
func checkDeltasEqual(c *gc.C, b multiwatcher.Backing, d0, d1 []params.Delta) {
	c.Check(deltaMap(d0, b), jc.DeepEquals, deltaMap(d1, b))
}

func deltaMap(deltas []params.Delta, b multiwatcher.Backing) map[interface{}]multiwatcher.EntityInfo {
	m := make(map[interface{}]multiwatcher.EntityInfo)
	for _, d := range deltas {
		id := d.Entity.EntityId()
		if d.Removed {
			m[id] = nil
		} else {
			m[id] = d.Entity
		}
	}
	return m
}

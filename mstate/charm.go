package mstate

import (
	"fmt"
	"launchpad.net/juju-core/charm"
	"net/url"
)

// charmDoc represents the internal state of a charm in MongoDB.
type charmDoc struct {
	URL          *charm.URL `bson:"_id"`
	Meta         *charm.Meta
	Config       *charm.Config
	BundleURL    string
	BundleSha256 string
}

// Charm represents the state of a charm in the environment.
type Charm struct {
	st   *State
	doc  charmDoc
	burl *url.URL
}

func newCharm(st *State, cdoc *charmDoc) (*Charm, error) {
	burl, err := url.Parse(cdoc.BundleURL)
	if err != nil {
		return nil, fmt.Errorf("cannot parse charm bundle URL: %q", cdoc.BundleURL)
	}
	return &Charm{st: st, doc: *cdoc, burl: burl}, nil
}

func (c *Charm) Refresh() error {
	doc := charmDoc{}
	err := c.st.charms.FindId(c.doc.URL).One(&doc)
	if err != nil {
		return fmt.Errorf("cannot refresh charm %v: %v", c, err)
	}
	c.doc = doc
	return nil
}

// URL returns the URL that identifies the charm.
func (c *Charm) URL() *charm.URL {
	clone := *c.doc.URL
	return &clone
}

// Revision returns the monotonically increasing charm 
// revision number.
func (c *Charm) Revision() int {
	return c.doc.URL.Revision
}

// Meta returns the metadata of the charm.
func (c *Charm) Meta() *charm.Meta {
	return c.doc.Meta
}

// Config returns the configuration of the charm.
func (c *Charm) Config() *charm.Config {
	return c.doc.Config
}

// BundleURL returns the url to the charm bundle in 
// the provider storage.
func (c *Charm) BundleURL() *url.URL {
	return c.burl
}

// BundleSha256 returns the SHA256 digest of the charm bundle bytes.
func (c *Charm) BundleSha256() string {
	return c.doc.BundleSha256
}

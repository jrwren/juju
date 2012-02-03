package ec2

import (
	. "launchpad.net/gocheck"
	"launchpad.net/goyaml"
	"regexp"
)

// Use local suite since this file lives in the ec2 package
// for testing internals.
type cloudinitSuite struct{}

var _ = Suite(cloudinitSuite{})

// Each test gives a cloudinit config - we check the
// output to see if it looks correct.
var cloudinitTests = []machineConfig{
	{
		adminSecret:        "topsecret",
		instanceIdAccessor: "$instance_id",
		machineId:          "aMachine",
		origin:             jujuOrigin{originBranch, "lp:jujubranch"},
		providerType:       "ec2",
		provisioner:        true,
		sshKeys:            []string{"sshkey1"},
		zookeeper:          true,
	},
	{
		adminSecret:    "topsecret",
		machineId:      "aMachine",
		origin:         jujuOrigin{originDistro, ""},
		providerType:   "ec2",
		provisioner:    false,
		sshKeys:        []string{"sshkey1"},
		zookeeper:      false,
		zookeeperHosts: []string{"zk1"},
	},
}

// cloundInitTest runs a set of tests for one of the machineConfig
// values above.
type cloudinitTest struct {
	x   map[interface{}]interface{} // the unmarshalled YAML.
	cfg *machineConfig                // the config being tested.
}

func (t *cloudinitTest) check(c *C) {
	t.checkPackage(c, "bzr")
	c.Check(t.x["apt_upgrade"], Equals, true)
	c.Check(t.x["apt_update"], Equals, true)
	t.checkScripts(c, "mkdir -p /var/lib/juju")
	t.checkMachineData(c)

	if t.cfg.zookeeper {
		t.checkPackage(c, "zookeeperd")
		t.checkScripts(c, "juju-admin initialize")
		t.checkScripts(c, regexp.QuoteMeta(t.cfg.instanceIdAccessor))
	}
	if t.cfg.origin != (jujuOrigin{}) && t.cfg.origin.origin == originDistro {
		t.checkScripts(c, "apt-get.*install juju")
	}
	if t.cfg.provisioner {
		t.checkScripts(c, "python -m juju.agents.provision")
	}
}

func (t *cloudinitTest) checkMachineData(c *C) {
	mdata0 := t.x["machine-data"]
	c.Assert(mdata0, NotNil)
	mdata := mdata0.(map[interface{}]interface{})
	m := mdata["machine-id"]
	c.Assert(m, Equals, t.cfg.machineId)
}

// checkScripts checks that at least one script started by
// the cloudinit matches the given regexp pattern.
func (t *cloudinitTest) checkScripts(c *C, pattern string) {
	scripts0 := t.x["runcmd"]
	if scripts0 == nil {
		c.Errorf("cloudinit has no entry for runcmd")
		return
	}
	scripts := scripts0.([]interface{})
	re := regexp.MustCompile(pattern)
	found := false
	for _, s0 := range scripts {
		s := s0.(string)
		if re.MatchString(s) {
			found = true
		}
	}
	if !found {
		c.Errorf("script %q not found", pattern)
	}
}

// checkPackage checks that the cloudinit will install the given package.
func (t *cloudinitTest) checkPackage(c *C, pkg string) {
	pkgs0 := t.x["packages"]
	if pkgs0 == nil {
		c.Errorf("cloudinit has no entry for packages")
		return
	}

	pkgs := pkgs0.([]interface{})

	found := false
	for _, p0 := range pkgs {
		p := p0.(string)
		if p == pkg {
			found = true
		}
	}
	if !found {
		c.Errorf("%q not found in packages", pkg)
	}
}

// TestCloudInit checks that the output from the various tests
// in cloudinitTests is well formed.
func (cloudinitSuite) TestCloudInit(c *C) {
	for i, cfg := range cloudinitTests {
		c.Logf("check %d", i)
		ci, err := newCloudInit(&cfg)
		c.Assert(err, IsNil)
		c.Check(ci, NotNil)

		// render the cloudinit config to bytes, and then
		// back to a map so we can introspect it without
		// worrying about internal details of the cloudinit
		// package.

		data, err := ci.Render()
		c.Assert(err, IsNil)

		x := make(map[interface{}]interface{})
		err = goyaml.Unmarshal(data, &x)
		c.Assert(err, IsNil)

		c.Logf("result %v", x)
		t := &cloudinitTest{
			cfg: &cfg,
			x:   x,
		}
		t.check(c)
	}
}

// When mutate is called on a known-good machineConfig,
// there should be an error complaining about the missing
// field named by the adjacent err.
var verifyTests = []struct {
	err    string
	mutate func(*machineConfig)
}{
	{"machine id", func(cfg *machineConfig) { cfg.machineId = "" }},
	{"provider type", func(cfg *machineConfig) { cfg.providerType = "" }},
	{"instance id accessor", func(cfg *machineConfig) {
		cfg.zookeeper = true
		cfg.instanceIdAccessor = ""
	}},
	{"admin secret", func(cfg *machineConfig) {
		cfg.zookeeper = true
		cfg.adminSecret = ""
	}},
	{"zookeeper hosts", func(cfg *machineConfig) {
		cfg.zookeeper = false
		cfg.zookeeperHosts = nil
	}},
}

// TestCloudInitVerify checks that required fields are appropriately
// checked for by newCloudInit.
func (cloudinitSuite) TestCloudInitVerify(c *C) {
	cfg := &machineConfig{
		provisioner:        true,
		zookeeper:          true,
		instanceIdAccessor: "$instance_id",
		adminSecret:        "topsecret",
		providerType:       "ec2",
		origin:             jujuOrigin{originBranch, "lp:jujubranch"},
		machineId:          "aMachine",
		sshKeys:            []string{"sshkey1"},
		zookeeperHosts:     []string{"zkhost"},
	}
	// check that the base configuration does not give an error
	_, err := newCloudInit(cfg)
	c.Assert(err, IsNil)

	for _, test := range verifyTests {
		cfg1 := *cfg
		test.mutate(&cfg1)
		t, err := newCloudInit(&cfg1)
		c.Assert(err, ErrorMatches, "invalid machine configuration: missing "+test.err)
		c.Assert(t, IsNil)
	}
}

var policyTests = []struct {
	policy string
	origin jujuOrigin
}{
	{`
		|juju:
		|  Installed: 0.5+bzr411-1juju1~natty1
		|  Candidate: 0.5+bzr411-1juju1~natty1
		|  Version table:
		| *** 0.5+bzr411-1juju1~natty1 0
		|        100 /var/lib/dpkg/status
		|     0.5+bzr398-0ubuntu1 0
		|        500 http://gb.archive.ubuntu.com/ubuntu/ oneiric/universe amd64 Packages`,
		jujuOrigin{
			originDistro,
			"",
		},
	},
	{`
		|juju:
		|  Installed: good-magic-1.0
		|  Candidate: good-magic-1.0
		|  Version table:
		| *** good-magic-1.0
		|        500 http://us.archive.ubuntu.com/ubuntu/ natty/main amd64 Packages
		|        100 /var/lib/dpkg/status`,
		jujuOrigin{originDistro, ""},
	}, {`
		|juju:
		|  Installed: good-magic-1.0
		|  Candidate: good-magic-1.0
		|  Version table:
		|     0.5+bzr366-1juju1~natty1 0
		|        500 http://ppa.launchpad.net/juju/pkgs/ubuntu/ natty/main amd64 Packages
		| *** good-magic-1.0 0
		|        500 http://us.archive.ubuntu.com/ubuntu/ natty/main amd64 Packages
		|        100 /var/lib/dpkg/status`,
		jujuOrigin{originDistro, ""},
	}, {`
		|juju:
		|  Installed: 0.5+bzr366-1juju1~natty1
		|  Candidate: 0.5+bzr366-1juju1~natty1
		|  Version table:
		|     bad-magic-0.5 0
		|        500 http://us.archive.ubuntu.com/ubuntu/ natty/main amd64 Packages
		| *** 0.5+bzr366-1juju1~natty1 0
		|        100 /var/lib/dpkg/status
		|        500 http://ppa.launchpad.net/juju/pkgs/ubuntu/ natty/main amd64 Packages
		|     0.5+bzr356-1juju1~natty1 0
		|        500 http://us.archive.ubuntu.com/ubuntu/ natty/main amd64 Packages`,
		jujuOrigin{originPPA, ""},
	}, {`
		|juju:
		|  Installed: (none)
		|  Candidate: good-magic-1.0
		|  Version table:
		|     0.5+bzr366-1juju1~natty1 0
		|        100 /var/lib/dpkg/status
		|        500 http://ppa.launchpad.net/juju/pkgs/ubuntu/ natty/main amd64 Packages
		|     good-magic-1.0 0
		|        500 http://us.archive.ubuntu.com/ubuntu/ natty/main amd64 Packages
		|        100 /var/lib/dpkg/status`,
		jujuOrigin{originBranch, "lp:juju"},
	}, {`
		|juju:
		|  Installed: 0.5+bzr356-1juju1~natty1
		|  Candidate: 0.5+bzr356-1juju1~natty1
		|  Version table:
		|     good-magic-1.0 0
		|        500 http://us.archive.ubuntu.com/ubuntu/ natty/main amd64 Packages
		| *** 0.5+bzr356-1juju1~natty1 0
		|        500 http://ppa.launchpad.net/juju/pkgs/ubuntu/ natty/main amd64 Packages
		|        100 /var/lib/dpkg/status`,
		jujuOrigin{originPPA, ""},
	}, {`
		|juju:
		|  Installed: whatever
		|  Candidate: whatever-else
		|  Nothing interesting here:`,
		jujuOrigin{originDistro, ""},
	}, {`
		|juju:
		|  Installed: good-magic-1.0
		|  Candidate: good-magic-1.0
		|  Version table:
		| *** good-magic-1.0 0
		|        500 http://ppa.launchpad.net/juju/pkgs/ubuntu/ natty/main amd64 Packages
		|        100 /var/lib/dpkg/status`,
		jujuOrigin{originPPA, ""},
	},
}

var unindentPattern = regexp.MustCompile(`\n\s*\|`)

// If the string doesn't start with a newline, unindent returns
// it. Otherwise it removes the initial newline and the
// indentation from each line of the string and adds a trailing newline.
// Indentation is defined to be
// a run of white space followed by a '|' character.
func unindent(s string) string {
	if s[0] != '\n' {
		return s
	}
	return unindentPattern.ReplaceAllString(s, "\n")[1:] + "\n"
}

func (cloudinitSuite) TestCloudPolicyToOrigin(c *C) {
	for i, t := range policyTests {
		o := policyToOrigin(unindent(t.policy) + "\n")
		c.Check(o, Equals, t.origin, Bug("test %d", i))
	}
}

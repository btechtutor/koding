package protocol

import (
	"github.com/koding/kloud/eventer"
	"github.com/koding/kloud/machinestate"
)

// Builder creates and provision a single image or machine for a given Provider.
type Builder interface {
	// Build the machine and creates an artifact that can be pass to other
	// methods
	Build(*Machine) (*Artifact, error)
}

// Controller manages a machine, it's start/stop/destroy/restart a machine.
type Controller interface {
	// Start starts the machine
	Start(*Machine) (*Artifact, error)

	// Stop stops the machine
	Stop(*Machine) error

	// Restart restarts the machine
	Restart(*Machine) error

	// Destroy destroys the machine
	Destroy(*Machine) error

	// Info returns full information about a single machine
	Info(*Machine) (*InfoArtifact, error)
}

// Machine is used as a context and data source for the appropriate interfaces
// provided by the Kloud package. A machine is gathered by the Storage
// interface.
type Machine struct {
	// MachineId defines a unique ID in which the build informations are
	// fetched from. MachineId is used to gather the Username, ImageName,
	// InstanceName etc.. For example it could be a mongodb object id that
	// would point to a document that carries those informations or a key for a
	// key/value storage.
	MachineId string

	// Provider defines the provider in which the data is used be
	Provider string

	// Builder contains information about how to build the data, like Username,
	// ImageName, InstanceName, Region, SSH KeyPair informations, etc...
	Builder map[string]interface{}

	// Credential contains information for accessing third party provider services
	Credential map[string]interface{}

	// Eventer pushes the latest events to the eventer hub. Anyone can listen
	// afterwards from the eventer hub.
	Eventer eventer.Eventer

	// CurrentData contains machines current data. This is needed sometimes to
	// update old records, creating domains based on pre defined labels,  etcc.
	// Basically put a
	CurrentData interface{}

	// State defines the machines current state
	State machinestate.State
}

// Artifact should be returned from a Build method. It contains data
// that is needed in other interfaces
type Artifact struct {
	// Machine Id defines the source of the build that caused this artifact to
	// be created. It should be equal to the MachineId that was passed via
	// MachineOptions
	MachineId string

	// InstanceName should define the name/hostname of the created machine. It
	// should be equal to the InstanceName that was passed via MachineOptions.
	InstanceName string

	// InstanceId should define a unique ID that defined the created machine.
	// It's different than the machineID and is usually an unique id which is
	// given by the third-party provider, for example DigitalOcean returns a
	// droplet Id, AWS returns an instance id, etc..
	InstanceId string

	// IpAddress defines the public ip address of the running machine.
	IpAddress string

	// DomainName defines the current domain record that is bound to the given
	// IpAddress
	DomainName string

	// Username defines the username to which the machine belongs.
	Username string

	// PrivateKey defines a private SSH key added to the machine. It's only
	// returned if the SSHKeyName and SSHPublicKey is defined in MachineOptions
	SSHPrivateKey string
	SSHUsername   string

	// KiteQuery is needed to find it via Kontrol
	KiteQuery string
}

// InfoArtifact should be returned from a Info method.
type InfoArtifact struct {
	// State defines the state of the machine
	State machinestate.State

	// Name defines the name of the machine.
	Name string
}

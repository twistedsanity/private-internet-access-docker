package mullvad

import (
	"github.com/qdm12/golibs/files"
	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/golibs/network"
	"github.com/qdm12/private-internet-access-docker/internal/models"
)

const logPrefix = "Mullvad configurator"

// Configurator contains methods to download, read and modify the openvpn configuration to connect as a client
type Configurator interface {
	GetOpenVPNConnections(country models.MullvadCountry, city models.MullvadCity, provider models.MullvadProvider, protocol models.NetworkProtocol, customPort uint16) (connections []models.OpenVPNConnection, err error)
	BuildConf(connections []models.OpenVPNConnection, verbosity, uid, gid int) (err error)
}

type configurator struct {
	client      network.Client
	fileManager files.FileManager
	logger      logging.Logger
}

// NewConfigurator returns a new Configurator object
func NewConfigurator(client network.Client, fileManager files.FileManager, logger logging.Logger) Configurator {
	return &configurator{client, fileManager, logger}
}
package minivmm

import (
	"os"
	"path/filepath"
)

const (
	// EnvDir is a environment variable key.
	EnvDir = "VMM_DIR"
	// EnvPort is a environment variable key.
	EnvPort = "VMM_LISTEN_PORT"
	// EnvOrigin is a environment variable key.
	EnvOrigin = "VMM_ORIGIN"
	// EnvOIDC is a environment variable key.
	EnvOIDC = "VMM_OIDC_URL"
	// EnvAgents is a environment variable key.
	EnvAgents = "VMM_AGENTS"
	// EnvCorsOrigins is a environment variable key.
	EnvCorsOrigins = "VMM_CORS_ALLOWED_ORIGINS"
	// EnvSubnetCIDR is a environment variable key.
	EnvSubnetCIDR = "VMM_SUBNET_CIDR"
	// EnvNameServers is a environment variable key.
	EnvNameServers = "VMM_NAME_SERVERS"
	// EnvServerCert is a environment variable key.
	EnvServerCert = "VMM_SERVER_CERT"
	// EnvServerKey is a environment variable key.
	EnvServerKey = "VMM_SERVER_KEY"
	// EnvNoTLS is a environment variable key.
	EnvNoTLS = "VMM_NO_TLS"
	// EnvNoAuth is a environment variable key.
	EnvNoAuth = "VMM_NO_AUTH"
	// EnvNoKvm is a environment variable key.
	EnvNoKvm = "VMM_NO_KVM"
	// EnvVNCKeyboardLayout is a environment variable key.
	EnvVNCKeyboardLayout = "VMM_VNC_KEYBOARD_LAYOUT"
)

var (
	// ForwardDir is a directory path for the fowarder's metadata files.
	ForwardDir = filepath.Join(os.Getenv(EnvDir), "forwards")
	// VMDir is a directory path for the files associated to virtual machines.
	VMDir = filepath.Join(os.Getenv(EnvDir), "vms")
	// ImageDir is a directory path for the base image files.
	ImageDir = filepath.Join(os.Getenv(EnvDir), "images")
)

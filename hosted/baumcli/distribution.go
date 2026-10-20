package baumcli

import (
	"os"
	"path/filepath"
)

// distribution names the build. The hosted service's release sets it to
// hostedDistribution at link time:
//
//	-X github.com/baum-ai/baum/hosted/baumcli.distribution=hosted
//
// The open-source release and local builds leave it empty.
var distribution string

const hostedDistribution = "hosted"

// hostedInstallMarkerName is the file the hosted service's install script
// writes beside the credentials, so a binary it installed counts as hosted
// even once hosted users get the same artefact as everyone else.
const hostedInstallMarkerName = "hosted-install"

// hostedInstall reports whether this baum was built or installed for the
// hosted service. Only then do the hooks set up Entire without a login in a
// repository whose committed settings ask for it; see hostedHooksEnabled.
func hostedInstall() bool {
	if distribution == hostedDistribution {
		return true
	}
	credentials, err := credentialsPath()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(credentials), hostedInstallMarkerName))
	return err == nil && info.Mode().IsRegular()
}

// Package setupcode holds the exit codes shared by orole-sandbox-setup.exe and the daemon.
package setupcode

const (
	OK             = 0    // Windows Hypervisor Platform is on; no restart needed
	Failed         = 1    // DISM failed or could not run
	Usage          = 2    // wrong arguments
	NotElevated    = 740  // ERROR_ELEVATION_REQUIRED: the helper was started without admin rights
	RebootRequired = 3010 // ERROR_SUCCESS_REBOOT_REQUIRED, as DISM reports it
)

// EnableWHPArg is the helper's only accepted argument.
const EnableWHPArg = "enable-whp"

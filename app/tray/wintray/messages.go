//go:build windows

package wintray

const (
	firstTimeTitle   = "ReEnvision AI is running"
	firstTimeMessage = "Click here to get started"
	updateTitle      = "Update available"
	updateMessage    = "ReEnvision AI version %s is ready to install"

	quitMenuTitle            = "Quit ReEnvision AI"
	updateAvailableMenuTitle = "An update is available"
	updateMenuTitle          = "Restart to update"
	diagLogsMenuTitle        = "View logs"
	startContainerTitle      = "Start"
	stopContainerTitle       = "Stop"
	modePrivateMenuTitle     = "Private (local only)"
	modeDistributedMenuTitle = "Distributed (grid)"
)

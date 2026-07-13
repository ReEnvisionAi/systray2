package commontray

var (
	Title   = "ReEnvision AI"
	Tooltip = "ReEnvision AI"

	UpdateIconName = "reai_update"
	IconName       = "reai"
)

type Callbacks struct {
	Quit               chan struct{}
	Update             chan struct{}
	DoFirstUse         chan struct{}
	ShowLogs           chan struct{}
	StartContainer     chan struct{}
	StopContainer      chan struct{}
	SetModePrivate     chan struct{}
	SetModeDistributed chan struct{}
}

type ReaiTray interface {
	GetCallbacks() Callbacks
	Run()
	UpdateAvailable(ver string) error
	DisplayFirstUseNotification() error
	ChangeStatusText(text string) error
	SetStarted() error
	SetStopped() error
	SetInferenceMode(mode string) error
	Quit()
}

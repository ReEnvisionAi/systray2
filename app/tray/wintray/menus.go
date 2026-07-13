//go:build windows

package wintray

import (
	"fmt"
	"log/slog"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	_ = iota
	statusMenuID
	statusSeparatorMenuID
	updateAvailableMenuID
	updateMenuID
	separatorMenuID
	startMenuID
	stopMenuID
	runSeparatorMenuID
	diagLogsMenuID
	diagSeparatorMenuID
	quitMenuID
	modeSeparatorMenuID
	modePrivateMenuID
	modeDistributedMenuID
)

func (t *winTray) initMenus() error {
	if err := t.addOrUpdateMenuItem(diagLogsMenuID, 0, diagLogsMenuTitle, false); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addSeparatorMenuItem(diagSeparatorMenuID, 0); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(quitMenuID, 0, quitMenuTitle, false); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addSeparatorMenuItem(modeSeparatorMenuID, 0); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(modeDistributedMenuID, 0, modeDistributedMenuTitle, false); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(modePrivateMenuID, 0, modePrivateMenuTitle, false); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(stopMenuID, 0, stopContainerTitle, true); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(startMenuID, 0, startContainerTitle, false); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addSeparatorMenuItem(runSeparatorMenuID, 0); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(statusMenuID, 0, "Status:", true); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addSeparatorMenuItem(statusSeparatorMenuID, 0); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}

	return nil
}

func (t *winTray) UpdateAvailable(ver string) error {
	if !t.updateNotified {
		slog.Debug("updating menu and sending notification for new update")
		if err := t.addOrUpdateMenuItem(updateAvailableMenuID, 0, updateAvailableMenuTitle, true); err != nil {
			return fmt.Errorf("unable to create menu entries %w", err)
		}
		if err := t.addOrUpdateMenuItem(updateMenuID, 0, updateMenuTitle, false); err != nil {
			return fmt.Errorf("unable to create menu entries %w", err)
		}
		if err := t.addSeparatorMenuItem(separatorMenuID, 0); err != nil {
			return fmt.Errorf("unable to create menu entries %w", err)
		}
		iconFilePath, err := iconBytesToFilePath(wt.updateIcon)
		if err != nil {
			return fmt.Errorf("unable to write icon data to temp file: %w", err)
		}
		if err := wt.setIcon(iconFilePath); err != nil {
			return fmt.Errorf("unable to set icon: %w", err)
		}
		t.updateNotified = true

		t.pendingUpdate = true
		// Now pop up the notification
		t.muNID.Lock()
		defer t.muNID.Unlock()
		copy(t.nid.InfoTitle[:], windows.StringToUTF16(updateTitle))
		copy(t.nid.Info[:], windows.StringToUTF16(fmt.Sprintf(updateMessage, ver)))
		t.nid.Flags |= NIF_INFO
		t.nid.Timeout = 10
		t.nid.Size = uint32(unsafe.Sizeof(*wt.nid))
		err = t.nid.modify()
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *winTray) ChangeStatusText(text string) error {
	if err := t.addOrUpdateMenuItem(statusMenuID, 0, "Status: "+text, true); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	return nil
}

func (t *winTray) SetStarted() error {
	if err := t.addOrUpdateMenuItem(startMenuID, 0, startContainerTitle, true); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(stopMenuID, 0, stopContainerTitle, false); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	return nil

}

// SetInferenceMode updates the mode menu items to reflect the active mode:
// the active entry gets a check prefix and is disabled (nothing to switch to).
func (t *winTray) SetInferenceMode(mode string) error {
	privTitle, distTitle := modePrivateMenuTitle, modeDistributedMenuTitle
	privActive := mode == "private"
	if privActive {
		privTitle = "✓ " + privTitle
	} else {
		distTitle = "✓ " + distTitle
	}
	if err := t.addOrUpdateMenuItem(modePrivateMenuID, 0, privTitle, privActive); err != nil {
		return fmt.Errorf("unable to update mode menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(modeDistributedMenuID, 0, distTitle, !privActive); err != nil {
		return fmt.Errorf("unable to update mode menu entries %w", err)
	}
	return nil
}

func (t *winTray) SetStopped() error {
	if err := t.addOrUpdateMenuItem(startMenuID, 0, startContainerTitle, false); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	if err := t.addOrUpdateMenuItem(stopMenuID, 0, stopContainerTitle, true); err != nil {
		return fmt.Errorf("unable to create menu entries %w", err)
	}
	return nil

}

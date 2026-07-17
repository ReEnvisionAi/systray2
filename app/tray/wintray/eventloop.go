package wintray

import (
	"fmt"
	"log/slog"
	"sync"
	"unsafe"

	"github.com/ReEnvision-AI/systray/app/power"
	"golang.org/x/sys/windows"
)

var quitOnce sync.Once

func (t *winTray) Run() {
	nativeLoop()
}

func nativeLoop() {
	m := &struct {
		WindowHandle windows.Handle
		Message      uint32
		Wparam       uintptr
		Lparam       uintptr
		Time         uint32
		Pt           point
		LPrivate     uint32
	}{}

	for {
		ret, _, err := pGetMessage.Call(uintptr(unsafe.Pointer(m)), 0, 0, 0)

		// If the function retrieves a message other than WM_QUIT, the return value is nonzero.
		// If the function retrieves the WM_QUIT message, the return value is zero.
		// If there is an error, the return value is -1
		// https://msdn.microsoft.com/en-us/library/windows/desktop/ms644936(v=vs.85).aspx
		switch int32(ret) {
		case -1:
			slog.Error(fmt.Sprintf("get message failure: %v", err))
			return
		case 0:
			return
		default:
			pTranslateMessage.Call(uintptr(unsafe.Pointer(m))) //nolint:errcheck
			pDispatchMessage.Call(uintptr(unsafe.Pointer(m)))  //nolint:errcheck
		}
	}
}

// WindowProc callback function that processes messages sent to a window.
// https://msdn.microsoft.com/en-us/library/windows/desktop/ms633573(v=vs.85).aspx
func (t *winTray) wndProc(hWnd windows.Handle, message uint32, wParam, lParam uintptr) (lResult uintptr) {
	const (
		WM_RBUTTONUP       = 0x0205
		WM_LBUTTONUP       = 0x0202
		WM_COMMAND         = 0x0111
		WM_ENDSESSION      = 0x0016
		WM_CLOSE           = 0x0010
		WM_DESTROY         = 0x0002
		WM_MOUSEMOVE       = 0x0200
		WM_LBUTTONDOWN     = 0x0201
		WM_POWERBROADCAST  = 0x0218
		PBT_APMSUSPEND     = 0x0004
		PBT_APMRESUMEAUTO  = 0x0012
		PBT_APMRESUMESUSPEND = 0x0007
	)
	switch message {
	case WM_COMMAND:
		menuItemId := int32(wParam)
		// https://docs.microsoft.com/en-us/windows/win32/menurc/wm-command#menus
		switch menuItemId {
		case quitMenuID:
			select {
			case t.callbacks.Quit <- struct{}{}:
			// should not happen but in case not listening
			default:
				slog.Error("no listener on Quit")
			}
		case updateMenuID:
			select {
			case t.callbacks.Update <- struct{}{}:
			// should not happen but in case not listening
			default:
				slog.Error("no listener on Update")
			}
		case diagLogsMenuID:
			select {
			case t.callbacks.ShowLogs <- struct{}{}:
			// should not happen but in case not listening
			default:
				slog.Error("no listener on ShowLogs")
			}
		case startMenuID:
			select {
			case t.callbacks.StartContainer <- struct{}{}:
			// should not happen but in case not listening
			default:
				slog.Error("no listener on StartContainer")
			}
		case stopMenuID:
			select {
			case t.callbacks.StopContainer <- struct{}{}:
			// should not happen but in case not listening
			default:
				slog.Error("no listener on StopContainer")
			}
		case modePrivateMenuID:
			select {
			case t.callbacks.SetModePrivate <- struct{}{}:
			// should not happen but in case not listening
			default:
				slog.Error("no listener on SetModePrivate")
			}
		case modeDistributedMenuID:
			select {
			case t.callbacks.SetModeDistributed <- struct{}{}:
			// should not happen but in case not listening
			default:
				slog.Error("no listener on SetModeDistributed")
			}
		default:
			slog.Debug("Unexpected menu item id", "id", menuItemId)
		}
	case WM_CLOSE:
		boolRet, _, err := pDestroyWindow.Call(uintptr(t.window))
		if boolRet == 0 {
			slog.Error("failed to destroy window", "error", err)
		}
		err = t.wcex.unregister()
		if err != nil {
			slog.Error("failed to unregister window", "error", err)
		}
	case WM_DESTROY:
		// same as WM_ENDSESSION, but throws 0 exit code after all
		defer pPostQuitMessage.Call(uintptr(int32(0))) //nolint:errcheck
		fallthrough
	case WM_ENDSESSION:
		t.muNID.Lock()
		if t.nid != nil {
			err := t.nid.delete()
			if err != nil {
				slog.Error("failed to delete nid", "error", err)
			}
		}
		t.muNID.Unlock()
	case t.wmSystrayMessage:
		switch lParam {
		case WM_MOUSEMOVE, WM_LBUTTONDOWN:
			// Ignore these...
		case WM_RBUTTONUP, WM_LBUTTONUP:
			err := t.showMenu()
			if err != nil {
				slog.Error("failed to show menu", "error", err)
			}
		case 0x405: // TODO - how is this magic value derived for the notification left click
			if t.pendingUpdate {
				select {
				case t.callbacks.Update <- struct{}{}:
				// should not happen but in case not listening
				default:
					slog.Error("no listener on Update")
				}
			} else {
				select {
				case t.callbacks.DoFirstUse <- struct{}{}:
				// should not happen but in case not listening
				default:
					slog.Error("no listener on DoFirstUse")
				}
			}
		case 0x404: // Middle click or close notification
			// slog.Debug("doing nothing on close of first time notification")
		default:
			// 0x402 also seems common - what is it?
			slog.Debug("unmanaged app message", "lParam", fmt.Sprintf("0x%x", lParam))
		}
	case t.wmTaskbarCreated: // on explorer.exe restarts
		t.muNID.Lock()
		err := t.nid.add()
		if err != nil {
			slog.Error("failed to refresh the taskbar on explorer restart", "error", err)
		}
		t.muNID.Unlock()
	case WM_POWERBROADCAST:
		power.HandlePowerBroadcast(wParam, lParam)
	default:
		// Calls the default window procedure to provide default processing for any window messages that an application does not process.
		// https://msdn.microsoft.com/en-us/library/windows/desktop/ms633572(v=vs.85).aspx
		lResult, _, _ = pDefWindowProc.Call(
			uintptr(hWnd),
			uintptr(message),
			wParam,
			lParam,
		)
	}
	return
}

func (t *winTray) Quit() {
	quitOnce.Do(quit)
}

func quit() {
	boolRet, _, err := pPostMessage.Call(
		uintptr(wt.window),
		WM_CLOSE,
		0,
		0,
	)
	if boolRet == 0 {
		slog.Error("failed to post close message on shutdown", "error", err)
	}
}

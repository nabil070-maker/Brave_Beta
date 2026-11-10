package main

// A small native Windows "setup wizard" window (pure Win32 via x/sys, no extra
// dependencies): welcome page -> progress page -> finish page (or error page).

import (
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	dUser32   = windows.NewLazySystemDLL("user32.dll")
	dGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	dComctl32 = windows.NewLazySystemDLL("comctl32.dll")
	dShell32  = windows.NewLazySystemDLL("shell32.dll")
	dKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassEx      = dUser32.NewProc("RegisterClassExW")
	pCreateWindowEx       = dUser32.NewProc("CreateWindowExW")
	pDefWindowProc        = dUser32.NewProc("DefWindowProcW")
	pGetMessage           = dUser32.NewProc("GetMessageW")
	pTranslateMessage     = dUser32.NewProc("TranslateMessage")
	pDispatchMessage      = dUser32.NewProc("DispatchMessageW")
	pIsDialogMessage      = dUser32.NewProc("IsDialogMessageW")
	pPostQuitMessage      = dUser32.NewProc("PostQuitMessage")
	pPostMessage          = dUser32.NewProc("PostMessageW")
	pSendMessage          = dUser32.NewProc("SendMessageW")
	pShowWindow           = dUser32.NewProc("ShowWindow")
	pUpdateWindow         = dUser32.NewProc("UpdateWindow")
	pDestroyWindow        = dUser32.NewProc("DestroyWindow")
	pEnableWindow         = dUser32.NewProc("EnableWindow")
	pSetWindowText        = dUser32.NewProc("SetWindowTextW")
	pGetSystemMetrics     = dUser32.NewProc("GetSystemMetrics")
	pLoadCursor           = dUser32.NewProc("LoadCursorW")
	pAdjustWindowRectEx   = dUser32.NewProc("AdjustWindowRectEx")
	pSetForegroundWindow  = dUser32.NewProc("SetForegroundWindow")
	pSetFocus             = dUser32.NewProc("SetFocus")
	pGetDpiForSystem      = dUser32.NewProc("GetDpiForSystem")
	pCreateFont           = dGdi32.NewProc("CreateFontW")
	pSetBkMode            = dGdi32.NewProc("SetBkMode")
	pGetStockObject       = dGdi32.NewProc("GetStockObject")
	pInitCommonControlsEx = dComctl32.NewProc("InitCommonControlsEx")
	pExtractIcon          = dShell32.NewProc("ExtractIconW")
	pGetModuleHandle      = dKernel32.NewProc("GetModuleHandleW")
)

const (
	wsChild       = 0x40000000
	wsVisible     = 0x10000000
	wsTabStop     = 0x00010000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000

	ssIcon         = 0x3
	ssEtchedHorz   = 0x10
	ssPathEllipsis = 0x8000
	bsDefPush      = 0x1
	bsAutoCheck    = 0x3
	pbsSmooth      = 0x1

	wmDestroy        = 0x0002
	wmSetFont        = 0x0030
	wmClose          = 0x0010
	wmCommand        = 0x0111
	wmCtlColorStatic = 0x0138
	wmApp            = 0x8000
	wmAppUpdate      = wmApp + 1
	wmAppDone        = wmApp + 2

	bmGetCheck = 0x00F0
	bmSetCheck = 0x00F1
	stmSetIcon = 0x0170
	pbmSetPos  = 0x0402

	idPrimary = 1 // IDOK: Enter key
	idCancel  = 2 // IDCANCEL: Esc key
	idCheck   = 3
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type winMsg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	ptX, ptY int32
	lPrivate uint32
}

type winRect struct{ left, top, right, bottom int32 }

type initCC struct{ size, icc uint32 }

type pageID int

const (
	pgWelcome pageID = iota
	pgInstalling
	pgDone
	pgError
)

var ui struct {
	hwnd, hinst                                                           uintptr
	dpi                                                                   int
	hTitle, hSub, hBody, hPath, hProg, hDetail, hCheck, hPrimary, hCancel uintptr
	page                                                                  pageID
	cfg                                                                   Config
	paths                                                                 Paths
	autoStart, installed, launch                                          bool
	lastBody, lastDetail                                                  string

	mu                            sync.Mutex
	phase, detail, errMsg, result string
	pct                           int
	cancelled                     bool
}

// ---------- small helpers ----------

func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func px(v int) uintptr { return uintptr(v * ui.dpi / 96) }

func setText(h uintptr, s string) {
	p := utf16(s)
	pSetWindowText.Call(h, uintptr(unsafe.Pointer(p)))
	runtime.KeepAlive(p)
}

func show(h uintptr, on bool) {
	cmd := uintptr(0)
	if on {
		cmd = 5
	}
	pShowWindow.Call(h, cmd)
}

func enable(h uintptr, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	pEnableWindow.Call(h, v)
}

func mkFont(pt, weight int) uintptr {
	h := -(pt * ui.dpi / 72)
	face := utf16("Segoe UI")
	r, _, _ := pCreateFont.Call(uintptr(int32(h)), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(face)))
	runtime.KeepAlive(face)
	return r
}

func setFont(h, f uintptr) { pSendMessage.Call(h, wmSetFont, f, 1) }

func ctl(class, text string, style uint32, x, y, w, h int, id uintptr) uintptr {
	c, t := utf16(class), utf16(text)
	r, _, _ := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(c)), uintptr(unsafe.Pointer(t)),
		uintptr(style|wsChild|wsVisible), px(x), px(y), px(w), px(h), ui.hwnd, id, ui.hinst, 0)
	runtime.KeepAlive(c)
	runtime.KeepAlive(t)
	return r
}

func postUpdate() { pPostMessage.Call(ui.hwnd, wmAppUpdate, 0, 0) }

// ---------- progress reporter used by the installer ----------

type uiReporter struct{}

func (uiReporter) Phase(s string) {
	log.Println(s)
	ui.mu.Lock()
	ui.phase = s
	ui.mu.Unlock()
	postUpdate()
}

func (uiReporter) Progress(pct int, d string) {
	ui.mu.Lock()
	ui.pct, ui.detail = pct, d
	ui.mu.Unlock()
	postUpdate()
}

func (uiReporter) Cancelled() bool {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.cancelled
}

// ---------- pages ----------

const welcomeText = "This wizard downloads the latest official Brave Beta and sets it up in the folder below.\n\n" +
	"Your profile, bookmarks and settings are kept in the same folder, so you can carry everything on your USB drive. " +
	"Nothing is installed on this PC."

func setPage(p pageID) {
	ui.page = p
	switch p {
	case pgWelcome:
		setText(ui.hBody, welcomeText)
		show(ui.hPath, true)
		show(ui.hProg, false)
		show(ui.hDetail, false)
		show(ui.hCheck, false)
		setText(ui.hPrimary, "Install")
		enable(ui.hPrimary, true)
		show(ui.hCancel, true)
		enable(ui.hCancel, true)
	case pgInstalling:
		ui.lastBody, ui.lastDetail = "", ""
		setText(ui.hBody, "Preparing…")
		show(ui.hPath, false)
		show(ui.hProg, true)
		show(ui.hDetail, true)
		show(ui.hCheck, false)
		setText(ui.hDetail, "")
		pSendMessage.Call(ui.hProg, pbmSetPos, 0, 0)
		setText(ui.hPrimary, "Installing…")
		enable(ui.hPrimary, false)
		show(ui.hCancel, true)
		enable(ui.hCancel, true)
	case pgDone:
		setText(ui.hBody, fmt.Sprintf("Brave Beta %s is ready.\n\nYour profile and settings will be kept in this folder.",
			installedVersion(ui.paths)))
		show(ui.hPath, false)
		show(ui.hProg, false)
		show(ui.hDetail, false)
		show(ui.hCheck, true)
		pSendMessage.Call(ui.hCheck, bmSetCheck, 1, 0)
		setText(ui.hPrimary, "Finish")
		enable(ui.hPrimary, true)
		show(ui.hCancel, false)
		pSetFocus.Call(ui.hPrimary)
	case pgError:
		ui.mu.Lock()
		msg := ui.errMsg
		ui.mu.Unlock()
		setText(ui.hBody, "Setup could not finish.\n\n"+msg+"\n\nDetails are in log\\brave-beta-portable.log.")
		show(ui.hPath, false)
		show(ui.hProg, false)
		show(ui.hDetail, false)
		show(ui.hCheck, false)
		setText(ui.hPrimary, "Close")
		enable(ui.hPrimary, true)
		show(ui.hCancel, false)
		pSetFocus.Call(ui.hPrimary)
	}
}

func startInstall() {
	setPage(pgInstalling)
	ui.mu.Lock()
	ui.phase, ui.detail, ui.pct, ui.cancelled, ui.result, ui.errMsg = "Preparing…", "", 0, false, "", ""
	ui.mu.Unlock()
	go func() {
		err := ensureLatestR(ui.cfg, ui.paths, false, uiReporter{})
		ui.mu.Lock()
		switch {
		case err == nil:
			ui.result = "ok"
		case errors.Is(err, errCancelled) || ui.cancelled:
			ui.result = "cancel"
		default:
			ui.result = "error"
			ui.errMsg = err.Error()
			log.Printf("install failed: %v", err)
		}
		ui.mu.Unlock()
		pPostMessage.Call(ui.hwnd, wmAppDone, 0, 0)
	}()
}

func applyUpdate() {
	if ui.page != pgInstalling {
		return
	}
	ui.mu.Lock()
	phase, detail, pct, cancelled := ui.phase, ui.detail, ui.pct, ui.cancelled
	ui.mu.Unlock()
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	if cancelled {
		phase = "Cancelling…"
	}
	if phase != ui.lastBody {
		ui.lastBody = phase
		setText(ui.hBody, phase)
	}
	if detail != ui.lastDetail {
		ui.lastDetail = detail
		setText(ui.hDetail, detail)
	}
	pSendMessage.Call(ui.hProg, pbmSetPos, uintptr(pct), 0)
}

func applyDone() {
	ui.mu.Lock()
	result := ui.result
	ui.mu.Unlock()
	switch result {
	case "ok":
		ui.installed = true
		if ui.autoStart {
			ui.launch = true
			pDestroyWindow.Call(ui.hwnd)
			return
		}
		setPage(pgDone)
	case "cancel":
		pDestroyWindow.Call(ui.hwnd)
	default:
		setPage(pgError)
	}
}

func onPrimary() {
	switch ui.page {
	case pgWelcome:
		startInstall()
	case pgDone:
		r, _, _ := pSendMessage.Call(ui.hCheck, bmGetCheck, 0, 0)
		ui.launch = r == 1
		pDestroyWindow.Call(ui.hwnd)
	case pgError:
		pDestroyWindow.Call(ui.hwnd)
	}
}

func onCancel() {
	if ui.page == pgInstalling {
		ui.mu.Lock()
		ui.cancelled = true
		ui.mu.Unlock()
		enable(ui.hCancel, false)
		postUpdate()
		return
	}
	pDestroyWindow.Call(ui.hwnd)
}

func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCommand:
		if wParam>>16 == 0 { // BN_CLICKED
			switch wParam & 0xFFFF {
			case idPrimary:
				onPrimary()
				return 0
			case idCancel:
				onCancel()
				return 0
			}
		}
	case wmClose:
		onCancel()
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	case wmCtlColorStatic:
		pSetBkMode.Call(wParam, 1)         // TRANSPARENT
		r, _, _ := pGetStockObject.Call(0) // WHITE_BRUSH
		return r
	case wmAppUpdate:
		applyUpdate()
		return 0
	case wmAppDone:
		applyDone()
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, msg, wParam, lParam)
	return r
}

// If the window cannot be created, install without a UI.
func fallbackInstall(cfg Config, p Paths) (bool, bool) {
	if err := ensureLatest(cfg, p, false); err != nil {
		msgBox("Brave Beta Portable", err.Error(), windows.MB_ICONERROR)
		return false, false
	}
	return true, true
}

// installerUI shows the setup window. With autoStart the welcome page is skipped
// (used for updates) and the window closes by itself when done.
// Returns whether Brave is installed and whether the user wants it launched.
func installerUI(cfg Config, p Paths, autoStart bool) (installed, launch bool) {
	runtime.LockOSThread()
	ui.cfg, ui.paths, ui.autoStart = cfg, p, autoStart

	ui.dpi = 96
	if pGetDpiForSystem.Find() == nil {
		if r, _, _ := pGetDpiForSystem.Call(); r >= 96 {
			ui.dpi = int(r)
		}
	}
	icc := initCC{icc: 0x20} // ICC_PROGRESS_CLASS
	icc.size = uint32(unsafe.Sizeof(icc))
	pInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))

	hm, _, _ := pGetModuleHandle.Call(0)
	ui.hinst = hm

	exe, _ := os.Executable()
	pe := utf16(exe)
	icon, _, _ := pExtractIcon.Call(ui.hinst, uintptr(unsafe.Pointer(pe)), 0)
	runtime.KeepAlive(pe)
	if icon == 1 {
		icon = 0
	}
	cursor, _, _ := pLoadCursor.Call(0, 32512) // IDC_ARROW

	className := utf16("BraveBetaPortableSetup")
	wc := wndClassEx{
		style:         3, // CS_HREDRAW | CS_VREDRAW
		lpfnWndProc:   windows.NewCallback(wndProc),
		hInstance:     ui.hinst,
		hIcon:         icon,
		hCursor:       cursor,
		hbrBackground: 6, // COLOR_WINDOW + 1
		lpszClassName: className,
		hIconSm:       icon,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		log.Printf("RegisterClassEx failed: %v", err)
		return fallbackInstall(cfg, p)
	}

	const clientW, clientH = 520, 316
	style := uint32(wsCaption | wsSysMenu | wsMinimizeBox)
	rc := winRect{0, 0, int32(px(clientW)), int32(px(clientH))}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&rc)), uintptr(style), 0, 0)
	ww, wh := int(rc.right-rc.left), int(rc.bottom-rc.top)
	sx, _, _ := pGetSystemMetrics.Call(0)
	sy, _, _ := pGetSystemMetrics.Call(1)
	x, y := (int(sx)-ww)/2, (int(sy)-wh)/2

	title := utf16("Brave Beta Portable Setup")
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		uintptr(style), uintptr(x), uintptr(y), uintptr(ww), uintptr(wh), 0, 0, ui.hinst, 0)
	runtime.KeepAlive(className)
	runtime.KeepAlive(title)
	if hwnd == 0 {
		log.Printf("CreateWindowEx failed: %v", err)
		return fallbackInstall(cfg, p)
	}
	ui.hwnd = hwnd

	fNormal := mkFont(9, 400)
	fTitle := mkFont(14, 700)

	if icon != 0 {
		hIcon := ctl("STATIC", "", ssIcon, 20, 16, 32, 32, 0)
		pSendMessage.Call(hIcon, stmSetIcon, icon, 0)
	}
	ui.hTitle = ctl("STATIC", "Brave Beta Portable Setup", 0, 64, 14, 436, 28, 0)
	setFont(ui.hTitle, fTitle)
	sub := ctl("STATIC", "Everything stays in this folder. Nothing is written to this PC.", 0, 64, 44, 436, 20, 0)
	setFont(sub, fNormal)
	ctl("STATIC", "", ssEtchedHorz, 0, 76, clientW, 2, 0)

	ui.hBody = ctl("STATIC", "", 0, 24, 92, 472, 96, 0)
	setFont(ui.hBody, fNormal)
	ui.hPath = ctl("STATIC", "Install location:  "+p.Root, ssPathEllipsis, 24, 200, 472, 20, 0)
	setFont(ui.hPath, fNormal)
	ui.hProg = ctl("msctls_progress32", "", pbsSmooth, 24, 200, 472, 20, 0)
	ui.hDetail = ctl("STATIC", "", 0, 24, 226, 472, 20, 0)
	setFont(ui.hDetail, fNormal)
	ui.hCheck = ctl("BUTTON", "Launch Brave Beta", bsAutoCheck|wsTabStop, 24, 222, 472, 24, idCheck)
	setFont(ui.hCheck, fNormal)

	ctl("STATIC", "", ssEtchedHorz, 0, 262, clientW, 2, 0)
	ui.hPrimary = ctl("BUTTON", "Install", bsDefPush|wsTabStop, 312, 274, 92, 28, idPrimary)
	setFont(ui.hPrimary, fNormal)
	ui.hCancel = ctl("BUTTON", "Cancel", wsTabStop, 412, 274, 92, 28, idCancel)
	setFont(ui.hCancel, fNormal)

	if autoStart {
		setText(ui.hTitle, "Updating Brave Beta")
		startInstall()
	} else {
		setPage(pgWelcome)
	}

	pShowWindow.Call(hwnd, 5)
	pUpdateWindow.Call(hwnd)
	pSetForegroundWindow.Call(hwnd)
	if !autoStart {
		pSetFocus.Call(ui.hPrimary)
	}

	var m winMsg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if handled, _, _ := pIsDialogMessage.Call(hwnd, uintptr(unsafe.Pointer(&m))); handled != 0 {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	return ui.installed, ui.launch
}

//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processRecord struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
	Port    string `json:"port"`
}

func processHandle(pid uint32, kill bool) (windows.Handle, error) {
	access := uint32(windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE)
	if kill {
		access |= windows.PROCESS_TERMINATE
	}
	return windows.OpenProcess(access, false, pid)
}

func processIdentity(h windows.Handle) (string, uint64, error) {
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", 0, err
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", 0, err
	}
	stamp := uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
	return windows.UTF16ToString(buf[:size]), stamp, nil
}

func inspectProcess(pid uint32) (uint64, error) {
	h, err := processHandle(pid, false)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	_, stamp, err := processIdentity(h)
	return stamp, err
}

func ownedProcess(rec processRecord, path string, terminate bool) bool {
	h, err := processHandle(rec.PID, terminate)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	exe, stamp, err := processIdentity(h)
	if err != nil || stamp != rec.Created || !strings.EqualFold(filepath.Clean(exe), filepath.Clean(path)) {
		return false
	}
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil || code != 259 {
		return false
	}
	if terminate {
		if windows.TerminateProcess(h, 0) != nil {
			return false
		}
		_, _ = windows.WaitForSingleObject(h, 5000)
	}
	return true
}

func hideProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000 | 0x00000008}
}

func openExternal(url string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url)
	hideProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
func reveal(path string) error {
	cmd := exec.Command("explorer.exe", path)
	hideProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

type dataBlob struct {
	Size uint32
	Data *byte
}

func cryptData(input []byte, encrypt bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, errors.New("empty credential")
	}
	in := dataBlob{Size: uint32(len(input)), Data: &input[0]}
	var out dataBlob
	name := "CryptUnprotectData"
	if encrypt {
		name = "CryptProtectData"
	}
	p := syscall.NewLazyDLL("crypt32.dll").NewProc(name)
	r, _, err := p.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}
func protect(b []byte) ([]byte, error)   { return cryptData(b, true) }
func unprotect(b []byte) ([]byte, error) { return cryptData(b, false) }

// Windows ignores POSIX 0600, so protect the new deployment's data directory
// with a user-only ACL before the upstream creates master.key or its database.
func secureDataDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	cmd := exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", "*"+user.User.Sid.String()+":(OI)(CI)F", "*S-1-5-18:(OI)(CI)F")
	hideProcess(cmd)
	return cmd.Run()
}

func desktopPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "@mirasimdesktop", "Mirasim.exe")
}
func desktopInstalled() bool { st, err := os.Stat(desktopPath()); return err == nil && !st.IsDir() }
func launchDesktop() error {
	if !desktopInstalled() {
		return errors.New("未找到已安装的 Mirasim 客户端")
	}
	cmd := exec.Command(desktopPath())
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

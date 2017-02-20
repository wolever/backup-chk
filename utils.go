package main

import (
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func ExpandUser(path string) (string, error) {
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}

	u, err := user.Current()
	if nil != err {
		return "", err
	}

	return strings.Replace(path, "~", u.HomeDir, 1), nil
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func FormatInt(n interface{}) string {
	var i64 int64
	i, ok := n.(int)
	if !ok {
		i64, ok = n.(int64)
		if !ok {
			return "ERROR: not an int"
		}
	} else {
		i64 = int64(i)
	}
	in := strconv.FormatInt(i64, 10)
	out := make([]byte, len(in)+(len(in)-2+int(in[0]/'0'))/3)
	if in[0] == '-' {
		in, out[0] = in[1:], '-'
	}

	for i, j, k := len(in)-1, len(out)-1, 0; ; i, j = i-1, j-1 {
		out[j] = in[i]
		if i == 0 {
			return string(out)
		}
		if k++; k == 3 {
			j, k = j-1, 0
			out[j] = ','
		}
	}
}

func GetWinSize() (int, int, error) {
	var ttyFd uintptr
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return -1, -1, err
	}
	defer tty.Close()

	ttyFd = tty.Fd()

	w := [4]uint16{0, 0, 0, 0}
	_, _, retval := syscall.Syscall(syscall.SYS_IOCTL,
		ttyFd,
		syscall.TIOCGWINSZ,
		uintptr(unsafe.Pointer(&w)),
	)
	runtime.GC()
	if retval != 0 {
		return -1, -1, err
	}
	return int(w[0]), int(w[1]), nil
}

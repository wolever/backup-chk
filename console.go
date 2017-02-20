package main

import (
	"fmt"
	"io"
	"os"
)

type BackupChkConsole struct {
	Stdout *os.File
	Stderr *os.File

	stdoutRead *os.File
	stderrRead *os.File

	realStdout *os.File
	realStderr *os.File

	needsNewline bool
}

func BackupChkConsoleInstallMonkeypatch() *BackupChkConsole {
	or, ow, err := os.Pipe()
	if err != nil {
		logger.Error("pipe error:", err)
		os.Exit(1)
	}

	er, ew, err := os.Pipe()
	if err != nil {
		logger.Error("pipe error:", err)
		os.Exit(1)
	}

	res := BackupChkConsole{
		Stdout: ow,
		Stderr: ew,

		stdoutRead: or,
		stderrRead: er,

		realStdout: os.Stdout,
		realStderr: os.Stderr,
	}

	os.Stdout = res.Stdout
	os.Stderr = res.Stderr

	go res.shuffleBytes(res.stdoutRead, res.realStdout)
	go res.shuffleBytes(res.stderrRead, res.realStderr)

	return &res
}

func (c *BackupChkConsole) Printf(format string, a ...interface{}) (int, error) {
	return fmt.Fprintf(c.Stdout, format, a...)
}

func (c *BackupChkConsole) shuffleBytes(src *os.File, dst *os.File) {
	buf := make([]byte, 1024)
	for {
		count, err := src.Read(buf)
		if err == io.EOF {
			return
		}
		if err != nil {
			logger.Error("Error shuffling bytes in:", err)
			c.Close()
			return
		}

		if len(buf) > 0 && buf[0] == '\r' {
			c.needsNewline = true
		} else if c.needsNewline {
			c.needsNewline = false
			dst.Write([]byte("\n"))
		}

		n, err := dst.Write(buf[:count])
		if n != count {
			logger.Error("Short write:", n, "!=", count)
			c.Close()
			return
		}
		if err != nil {
			logger.Error("Error shuffling bytes out:", err)
			c.Close()
			return
		}
	}
}

func (c *BackupChkConsole) Close() {
	if c.realStderr == nil {
		return
	}

	os.Stdout = c.realStdout
	os.Stderr = c.realStderr

	c.realStdout = nil
	c.realStderr = nil

	c.Stdout.Close()
	c.Stderr.Close()

	c.stdoutRead.Close()
	c.stderrRead.Close()
}

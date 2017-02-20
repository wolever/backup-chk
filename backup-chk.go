package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/alexcesaro/log"
	"github.com/alexcesaro/log/golog"
	flags "github.com/jessevdk/go-flags"
)

var ERR_NOT_DIR = errors.New("not a directory")

var logger *golog.Logger

var TOTAL_BYTES_READ uint64 = 0

type WalkerItem struct {
	root        *string
	path        string
	err         error
	stat        *os.FileInfo
	file        *os.File
	_relpath    *string
	SkipReaddir bool
}

func WalkerItemFromFile(root *string, path string, stat *os.FileInfo) *WalkerItem {
	return &WalkerItem{
		root: root,
		path: path,
		stat: stat,
	}
}

func WalkerItemFromRoot(root string) (*WalkerItem, error) {
	stat, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}

	if !stat.IsDir() {
		return nil, ERR_NOT_DIR
	}

	return WalkerItemFromFile(&root, root, &stat), nil
}

func (i *WalkerItem) makeStat() {
	if i.stat == nil && i.err == nil {
		stat, err := os.Lstat(i.path)
		i.err = err
		if err == nil {
			i.stat = &stat
		} else {
			i.stat = nil
		}
	}
}

func (i *WalkerItem) RelPath() string {
	if i._relpath == nil {
		prefix := i.path[len(*i.root):]
		if len(prefix) > 0 && prefix[:1] == "/" {
			prefix = prefix[1:]
		}
		i._relpath = &prefix
	}
	return *i._relpath
}

func (i *WalkerItem) Err() error {
	i.makeStat()
	return i.err
}

func (i *WalkerItem) GetItem(ref *WalkerItem) WalkerItem {
	return WalkerItem{
		root: i.root,
		path: path.Join(*i.root, ref.RelPath()),
	}
}

func (i *WalkerItem) IsDir() bool {
	i.makeStat()
	if i.stat == nil {
		return false
	}
	return (*i.stat).IsDir()
}

func (i *WalkerItem) Stat() (*os.FileInfo, error) {
	i.makeStat()
	return i.stat, i.err
}

func (i *WalkerItem) Readlink() (string, error) {
	return os.Readlink(i.path)
}

func (i *WalkerItem) Open() (*os.File, error) {
	if i.file == nil {
		file, err := os.Open(i.path)
		if err != nil {
			return nil, err
		}
		i.file = file
	}
	return i.file, nil
}

func (i *WalkerItem) Close() {
	if i.file != nil {
		i.file.Close()
		i.file = nil
	}
}

func (i *WalkerItem) Readdir(n int) ([]*WalkerItem, error) {
	if !i.IsDir() {
		return nil, ERR_NOT_DIR
	}
	if i.file == nil {
		return nil, errors.New("Must Open before Readdir")
	}
	files, err := i.file.Readdir(n)
	if err != nil {
		return nil, err
	}
	res := make([]*WalkerItem, len(files))
	for idx, f := range files {
		fCopy := f
		res[idx] = WalkerItemFromFile(i.root, path.Join(i.path, f.Name()), &fCopy)
	}
	return res, nil
}

type DFWalker struct {
	logfile      *os.File
	logfileDirty *time.Time
	logfilePos   int

	root    *WalkerItem
	stack   []*WalkerItem
	exclude pathMatchFn

	closeLock sync.Mutex
}

type pathMatchFn *func(string) bool

func NewDFWalker(configDir string, root *WalkerItem, exclude pathMatchFn) (*DFWalker, error) {
	logfile, err := os.OpenFile(
		path.Join(configDir, "walk-stack"),
		os.O_APPEND|os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}

	offset, err := logfile.Seek(0, os.SEEK_END)
	if err != nil {
		return nil, err
	}

	stack := []*WalkerItem{root}

	if offset > 0 {
		logger.Info("Loading previous run state from cache...")
		_, err := logfile.Seek(0, os.SEEK_SET)
		if err != nil {
			return nil, err
		}

		scanner := bufio.NewScanner(logfile)
		for scanner.Scan() {
			line := strings.Trim(scanner.Text(), " \n")
			if len(line) == 0 {
				continue
			}
			item := WalkerItemFromFile(
				&root.path,
				path.Join(root.path, scanner.Text()),
				nil)
			item.SkipReaddir = true
			stack = append(stack, item)
		}
	}

	return &DFWalker{
		logfile: logfile,
		root:    root,
		stack:   stack,
		exclude: exclude,
	}, nil
}

func (w *DFWalker) flush() {
	logfile := w.logfile
	if logfile == nil {
		return
	}

	_, err := logfile.Seek(0, os.SEEK_SET)
	if err != nil {
		logger.Errorf("Error flushing log file (it should be removed): %s", err)
		return
	}

	err = logfile.Truncate(0)
	if err != nil {
		logger.Errorf("Error truncating log file (it should be removed): %s", err)
		return
	}

	if len(w.stack) == 0 {
		return
	}

	logger.Info("Flushing walker stack to", logfile.Name())
	for _, item := range w.stack {
		logfile.Write([]byte(item.RelPath() + "\n"))
	}
}

func (w *DFWalker) Close() {
	if w.logfile != nil {
		w.closeLock.Lock()
		defer w.closeLock.Unlock()
		if w.logfile != nil {
			w.flush()
			(*w.logfile).Close()
			w.logfile = nil
		}
	}
}

func (w *DFWalker) stackPop() *WalkerItem {
	if len(w.stack) == 0 {
		return nil
	}
	item := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	return item
}

func (w *DFWalker) Next() (*WalkerItem, error) {
	item := w.stackPop()
	if item == nil {
		return nil, nil
	}

	if item.Err() != nil {
		return nil, item.Err()
	}

	if item.IsDir() && !item.SkipReaddir {
		_, err := item.Open()
		if err != nil {
			return nil, err
		}
		defer item.Close()

		for {
			contents, err := item.Readdir(1000)
			if err != nil && err != io.EOF {
				return nil, err
			}
			for _, p := range contents {
				if w.exclude != nil && (*w.exclude)(p.RelPath()) {
					continue
				}
				w.stack = append(w.stack, p)
			}

			if len(contents) == 0 {
				break
			}
		}
	}

	return item, nil
}

func checkError(reference interface{}, backup interface{}, msg string) error {
	return errors.New(fmt.Sprintf("%s: reference %v != backup %v",
		msg, reference, backup))
}

func check(refItem *WalkerItem, bckItem *WalkerItem) error {
	bckPtr, err := bckItem.Stat()
	if err != nil {
		return err
	}

	refPtr, err := refItem.Stat()
	if err != nil {
		return err
	}

	bck := *bckPtr
	ref := *refPtr

	if ref.ModTime().After(bck.ModTime()) {
		return nil
	}

	if ref.IsDir() != bck.IsDir() {
		return checkError(ref.IsDir(), bck.IsDir(), "IsDir does not match")
	}

	if bck.IsDir() {
		return nil
	}

	if ref.Mode() != bck.Mode() {
		return checkError(ref.Mode(), bck.Mode(), "mode mismatch")
	}

	rIsSymlink := ref.Mode()&os.ModeType == os.ModeSymlink
	bIsSymlink := bck.Mode()&os.ModeType == os.ModeSymlink
	if rIsSymlink != bIsSymlink {
		return checkError(rIsSymlink, bIsSymlink, "IsSymlink mismatch")
	}

	if rIsSymlink {
		rLink, err := refItem.Readlink()
		if err != nil {
			return err
		}

		bLink, err := bckItem.Readlink()
		if err != nil {
			return err
		}

		if rLink != bLink {
			return checkError(rLink, bLink, "symlink target mismatch")
		}

		return nil
	}

	if ref.Size() != bck.Size() {
		return checkError(FormatInt(ref.Size()), FormatInt(bck.Size()), "size do not match")
	}

	rf, err := refItem.Open()
	if err != nil {
		return err
	}
	defer refItem.Close()

	bf, err := bckItem.Open()
	if err != nil {
		return err
	}
	defer bckItem.Close()

	chunkSize := 4096
	rChunk := make([]byte, chunkSize)
	bChunk := make([]byte, chunkSize)
	chunkNum := 0
	for {
		chunkNum += 1
		rSz, err := rf.Read(rChunk)
		if err != nil && err != io.EOF {
			return err
		}

		bSz, err := bf.Read(bChunk)
		if err != nil && err != io.EOF {
			return err
		}

		TOTAL_BYTES_READ += uint64(rSz)

		if bytes.Compare(rChunk, bChunk) != 0 {
			return checkError(
				fmt.Sprintf("(chunk of size %d)", rSz),
				fmt.Sprintf("(chunk of size %d)", bSz),
				fmt.Sprintf("chunk mismatch at offset %d", (chunkNum-1)*chunkSize))
		}

		if err == io.EOF {
			break
		}

	}

	return nil
}

type CmdlineOptions struct {
	Verbose     []bool   `short:"v" long:"verbose" description:"Show verbose debug information"`
	TimeMachine bool     `short:"t" long:"time-machine" description:"Use Time Machine defaults"`
	Exclude     []string `short:"x" long:"exclude" description:"Exclude files with relative paths matching this pattern. Matching is simple glob matching (ex, 'foo*bar' matches 'foo/x/bar', 'foobar', and 'foo-bar')"`
	ConfigDir   string   `short:"c" long:"config-dir" default:"~/.backup-chk/" description:"Configuration and status directory"`
}

type TMGuess struct {
	NoTM      bool
	Unmounted bool
	Invalid   bool

	Okay           bool
	Directory      *string
	HomeVolumeName *string
}

func darwinGetRootDeviceName() (string, error) {
	outBytes, err := exec.Command("diskutil", "info", "-plist", "/").CombinedOutput()
	if err != nil {
		return "", err
	}

	type Node struct {
		XMLName xml.Name
		Content string `xml:",chardata"`
		Nodes   []Node `xml:",any"`
	}

	r := strings.NewReader(string(outBytes))
	d := xml.NewDecoder(r)

	n := Node{}
	d.Decode(&n)

	walk := func(node Node, prev Node) (string, error) { return "", nil }
	walk = func(node Node, prev Node) (string, error) {
		if prev.XMLName.Local == "key" && prev.Content == "VolumeName" {
			if node.XMLName.Local != "string" {
				return "", errors.New("error parsing plist (got unexpected node after VolumeName: " + node.XMLName.Local + ")")
			}
			return string(node.Content), nil
		}
		for _, child := range node.Nodes {
			res, err := walk(child, prev)
			if err != nil {
				return "", err
			}
			if len(res) > 0 {
				return res, nil
			}
			prev = child
		}
		return "", nil
	}
	res, err := walk(n, n)
	if len(res) == 0 {
		return "", errors.New("Could not find VolumeName in diskutil output")
	}
	return res, nil
}

func guessTimeMachineBackup() *TMGuess {
	outBytes, err := exec.Command("tmutil", "latestbackup").CombinedOutput()

	out := strings.Trim(string(outBytes), " \n")
	if !strings.HasPrefix(out, "/") {
		logger.Infof("tmutil unexpected output: %s (not guessing default directories)", out)
		return &TMGuess{
			Unmounted: true,
		}
	}

	if err != nil {
		logger.Infof("tmutil returned error: %s (not guessing default directories)", err)
		return &TMGuess{
			NoTM: true,
		}
	}

	st, err := os.Stat(out)
	if err != nil || !st.IsDir() {
		logger.Infof("tmutil did not return a directory: %s (not guessing default directories)", out)
		return &TMGuess{
			Invalid: true,
		}
	}

	// For now, assume /Users is a directory of / and not a mountpoint
	homeVolName, err := darwinGetRootDeviceName()
	if err != nil {
		logger.Info("error looking up root device name:", err)
		return &TMGuess{
			Invalid: true,
		}
	}

	return &TMGuess{
		Okay: true,

		Directory:      &out,
		HomeVolumeName: &homeVolName,
	}
}

func showTimeMachineHelp(g *TMGuess, indent string) {
	if g.NoTM {
		fmt.Println(indent + "Time Machine (tmutil) was not found.")
	} else if g.Unmounted {
		fmt.Println(indent + "Time Machine backup isn't mounted.")
	} else if g.Invalid {
		fmt.Println(indent + "tmutil returned an invalid path (run with -v for more info).")
	} else if g.Directory != nil {
		fmt.Println(indent + "This Time Machine backup will be used: " + *g.Directory)
	}
}

func buildExcludeFunc(rawPats []string) (pathMatchFn, error) {
	if len(rawPats) == 0 {
		return nil, nil
	}

	pats := make([][]string, len(rawPats))
	for idx, pat := range rawPats {
		pat = strings.Trim(pat, "*")
		pats[idx] = strings.Split(pat, "*")
	}

	excludeFunc := func(p string) bool {
		for patIdx, pat := range pats {
			loc := 0
			for _, patBit := range pat {
				loc = strings.Index(p[loc:], patBit)
				if loc < 0 {
					break
				}
			}

			if loc >= 0 {
				if patIdx > 0 {
					pats[patIdx-1], pats[patIdx] = pats[patIdx], pats[patIdx-1]
				}
				return true
			}
		}

		return false
	}

	return &excludeFunc, nil
}

func _main() int {
	opts := CmdlineOptions{}
	parser := flags.NewParser(&opts, flags.Default)
	parser.Usage = "[OPTIONS] [-vv] [--time-machine] [REFERENCE_DIR:BACKUP_DIR ...]"
	args, err := parser.Parse()

	// Setup console
	c := BackupChkConsoleInstallMonkeypatch()
	defer c.Close()

	// Setup logging
	var logLevels = []log.Level{
		log.Warning,
		log.Info,
		log.Debug,
	}
	logLevel := logLevels[min(len(opts.Verbose), len(logLevels)-1)]
	logger = golog.New(os.Stderr, log.Debug)
	logger.Writer = func(out io.Writer, logLine []byte, level log.Level) {
		if level <= logLevel {
			out.Write(logLine)
		}
	}

	// Check for TimeMachine
	tmGuess := (*TMGuess)(nil)
	if opts.TimeMachine {
		tmGuess = guessTimeMachineBackup()
		if (*tmGuess).Directory != nil {
			args = []string{"/Users/:" + path.Join(*tmGuess.Directory, *tmGuess.HomeVolumeName, "Users")}
		}
		if !tmGuess.Okay {
			showTimeMachineHelp(tmGuess, "Error: ")
			return 1
		}
		opts.Exclude = append(
			opts.Exclude,
			".Trash",

			"Library/Logs",
			"Library/Cache",
			"Library/Saved Application State",
			"Library/Application Support/*/Cache",
			"Library/Calendars/Calendar Cache",

			// iTunes
			"Album Artwork/Cache",
			"Album Artwork/Store",
			"Album Artwork/Generated",

			// iPhoto
			"CloudSync.noindex", // In private/com.apple.cloudphotosd
			".photoslibrary/database",
			"Library/Containers/com.apple.cloudphotosd/*cloudphotoservicelibrary/database",

			// XCode
			"Xcode/UserData/IB Support/Simulator Devices",
			"Xcode/DerivedData",
			"CoreSimulator/Devices",
		)
	}

	// Show help
	if err != nil || args == nil || len(args) == 0 {
		if err == nil {
			parser.WriteHelp(c.Stdout)
			fmt.Print("\n")
		} else {
			fmt.Println(err)
		}

		fmt.Println("Example:")
		argv0 := os.Args[0]
		if len(argv0) > 15 {
			argv0 = "..." + argv0[len(argv0)-15:]
		}
		fmt.Printf("  $ %s --time-machine\n", argv0)
		fmt.Printf("  $ %s /Users/wolever:/Volumes/Backup/Users/wolever\n", argv0)
		if tmGuess != nil {
			fmt.Printf("\nTime Machine:\n")
			showTimeMachineHelp(tmGuess, "  ")
		}
		fmt.Printf("\n")
		return 1
	}

	// Parse ref:bck pairs
	type Pair struct {
		ref *WalkerItem
		bck *WalkerItem
	}

	pairs := make([]Pair, len(args))
	for idx, backup := range args {
		pair := strings.Split(backup, ":")
		if len(pair) != 2 {
			logger.Errorf("invalid REFERENCE_DIR:BACKUP_DIR pair: %s (hint: /Users/:/Volumes/Backup/Users)", backup)
			return 1
		}

		refRoot, err := WalkerItemFromRoot(pair[0])
		if err != nil {
			logger.Error(err)
			return 1
		}

		bckRoot, err := WalkerItemFromRoot(pair[1])
		if err != nil {
			logger.Error(err)
			return 1
		}

		pairs[idx] = Pair{refRoot, bckRoot}
	}

	// Setup exclude function
	excludeFunc, err := buildExcludeFunc(opts.Exclude)
	if err != nil {
		logger.Error(err)
		return 1
	}

	// Initialize config directory
	configDir, err := ExpandUser(opts.ConfigDir)
	if err != nil {
		logger.Error(err)
		return 1
	}
	os.Mkdir(configDir, 0700)

	// Setup signal handling
	var walker *DFWalker
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		if walker != nil {
			walker.Close()
		}
		c.Close()
		os.Exit(1)
	}()

	// Actually check things!
	startTime := time.Now()
	for _, pair := range pairs {
		logger.Infof("Checking: '%s' against '%s'", *pair.bck.root, *pair.ref.root)

		// Setup status directory
		refNorm, err := filepath.Abs(*pair.ref.root)
		if err != nil {
			logger.Error(err)
			return 1
		}
		refNorm = filepath.Clean(refNorm)
		refNorm = strings.Replace(refNorm, "-", "--", -1)
		refNorm = strings.Replace(refNorm, "/", "-", -1)[1:]
		runStatusDir := path.Join(configDir, "run-status", refNorm)
		os.MkdirAll(runStatusDir, 0700)

		// Setup logging
		sessionLogFileName := path.Join(runStatusDir, "log.txt")
		sessionLogFile, err := os.OpenFile(
			sessionLogFileName,
			os.O_APPEND|os.O_WRONLY|os.O_CREATE,
			0600)

		origWriter := logger.Writer
		logCleanup := func() {
			if sessionLogFile == nil {
				return
			}
			logger.Writer = origWriter
			sessionLogFile.Close()
			sessionLogFile = nil
		}
		defer logCleanup()

		if err != nil {
			logger.Errorf("Error opening session log file: %s", err)
		} else {
			sessionLogFile.Write([]byte("Starting session...\n"))
			logger.Writer = func(out io.Writer, logLine []byte, level log.Level) {
				if level <= logLevel {
					out.Write(logLine)
				}
				if sessionLogFile != nil && level < log.Debug {
					sessionLogFile.Write(logLine)
				}
			}
		}

		// Setup walker
		walker, err = NewDFWalker(runStatusDir, pair.ref, excludeFunc)
		defer walker.Close()
		if err != nil {
			logger.Error(err)
			return 1
		}

		count := 0
		errCount := 0
		lastTime := time.Time{}
		for {
			refItem, err := walker.Next()
			if err != nil {
				logger.Error(err)
				return 1
			}

			if refItem == nil {
				break
			}

			if refItem.Err() != nil {
				logger.Error("ASSERITON ERROR: refItem.Err() should not be nil")
				logger.Error(refItem.Err())
				return 1
			}

			bckItem := pair.bck.GetItem(refItem)
			if logLevel >= log.Debug {
				logger.Debug("Checking", bckItem.RelPath())
			}

			err = check(refItem, &bckItem)
			if err != nil {
				logger.Warningf("%s: %s", refItem.RelPath(), err)
				errCount += 1
			}
			count += 1

			if logLevel >= log.Warning && !bckItem.IsDir() {
				now := time.Now()
				if now.Sub(lastTime).Seconds() > 3 {
					lastTime = now
					rate := float64(TOTAL_BYTES_READ) / now.Sub(startTime).Seconds() / 1024.0 / 1024.0
					msg := fmt.Sprintf("%s checked / %s errors @ %0.02fGB/s",
						FormatInt(count),
						FormatInt(errCount),
						rate,
					)

					_, cols, _ := GetWinSize()
					path := bckItem.RelPath()
					if cols > 0 {
						width := cols - len(msg) - 3
						if len(path) > width {
							path = "..." + path[len(path)-width+3:]
						}
					}

					c.Printf("\r\033[2K%s (%s)", msg, path)
				}
			}
		}

		now := time.Now()
		duration := now.Sub(startTime)
		rate := float64(TOTAL_BYTES_READ) / duration.Seconds() / 1024.0 / 1024.0
		logger.Infof(
			"Finished! %s checked, %s errors, and %s bytes in %v (%0.0f files/s, %0.02fGB/s)",
			FormatInt(count),
			FormatInt(errCount),
			FormatInt(int64(TOTAL_BYTES_READ)),
			duration,
			float64(count)/duration.Seconds(),
			rate,
		)

		walker.Close()
		if errCount > 0 && sessionLogFile != nil {
			logCleanup()
			logger.Warning("Errors logged to:", sessionLogFileName)
		} else {
			logCleanup()
		}
	}

	return 0
}

func main() {
	os.Exit(_main())
}

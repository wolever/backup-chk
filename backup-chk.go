package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/alexcesaro/log"
	"github.com/alexcesaro/log/golog"
	flags "github.com/jessevdk/go-flags"
)

var ERR_NOT_DIR = errors.New("not a directory")

var logger *golog.Logger

type WalkerItem struct {
	root     *string
	path     string
	err      error
	stat     *os.FileInfo
	file     *os.File
	_relpath *string
}

func WalkerItemFromFile(root *string, path string, stat os.FileInfo) *WalkerItem {
	return &WalkerItem{
		root: root,
		path: path,
		stat: &stat,
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

	return WalkerItemFromFile(&root, root, stat), nil
}

func (i *WalkerItem) makeStat() {
	if i.stat == nil && i.err == nil {
		stat, err := os.Lstat(i.path)
		i.err = err
		i.stat = &stat
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
	return i.stat != nil && (*i.stat).IsDir()
}

func (i *WalkerItem) Stat() (os.FileInfo, error) {
	i.makeStat()
	return *i.stat, i.err
}

func (i *WalkerItem) Readlink() (string, error) {
	return os.Readlink(i.path)
}

func (i *WalkerItem) Open() (*os.File, error) {
	return os.Open(i.path)
}

func (i *WalkerItem) Readdir(n int) ([]*WalkerItem, error) {
	if !i.IsDir() {
		return nil, ERR_NOT_DIR
	}
	if i.file == nil {
		file, err := os.Open(i.path)
		if err != nil {
			return nil, err
		}
		i.file = file
	}
	files, err := i.file.Readdir(n)
	if err != nil {
		return nil, err
	}
	res := make([]*WalkerItem, len(files))
	for idx, f := range files {
		res[idx] = WalkerItemFromFile(i.root, path.Join(i.path, f.Name()), f)
	}
	return res, nil
}

type DFWalker struct {
	root  *WalkerItem
	stack []*WalkerItem
}

func NewDFWalker(root *WalkerItem) *DFWalker {
	stack := make([]*WalkerItem, 1)
	stack[0] = root
	return &DFWalker{
		root:  root,
		stack: stack,
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

	if item.IsDir() {
		for {
			contents, err := item.Readdir(1000)
			if err != nil && err != io.EOF {
				return nil, err
			}
			w.stack = append(w.stack, contents...)

			if len(contents) == 0 {
				break
			}
		}
	}

	return item, nil
}

func checkError(reference interface{}, backup interface{}, msg string) error {
	return errors.New(fmt.Sprintf("%s: reference %s != backup %s",
		msg, reference, backup))
}

func check(refItem *WalkerItem, bckItem WalkerItem) error {
	bck, err := bckItem.Stat()
	if err != nil {
		return err
	}

	ref, err := refItem.Stat()
	if err != nil {
		return err
	}

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
		return checkError(ref.Size(), bck.Size(), "size do not match")
	}

	rf, err := refItem.Open()
	if err != nil {
		return err
	}
	defer rf.Close()

	bf, err := bckItem.Open()
	if err != nil {
		return err
	}
	defer bf.Close()

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
	Verbose []bool `short:"v" long:"verbose" description:"Show verbose debug information"`
}

type TMGuess struct {
	NoTM      bool
	Unmounted bool
	Invalid   bool
	Directory *string
}

func guessTimeMachineBackup() *TMGuess {
	outBytes, err := exec.Command("tmutil", "latestbackup").CombinedOutput()

	out := strings.Trim(string(outBytes), " \n")
	if !strings.HasPrefix(out, "/") {
		logger.Debugf("tmutil unexpected output: %s (not guessing default directories)", out)
		return &TMGuess{
			Unmounted: true,
		}
	}

	if err != nil {
		logger.Debugf("tmutil returned error: %s (not guessing default directories)", err)
		return &TMGuess{
			NoTM: true,
		}
	}

	st, err := os.Stat(out)
	if err != nil || !st.IsDir() {
		logger.Debugf("tmutil did not return a directory: %s (not guessing default directories)", out)
		return &TMGuess{
			Invalid: true,
		}
	}
	return &TMGuess{
		Directory: &out,
	}
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func main() {
	opts := CmdlineOptions{}
	parser := flags.NewParser(&opts, flags.Default)
	parser.Usage = "[-v] REFERENCE_DIR:BACKUP_DIR ..."
	args, err := parser.Parse()

	var logLevels = []log.Level{
		log.Warning,
		log.Info,
		log.Debug,
	}
	logLevel := logLevels[min(len(opts.Verbose), len(logLevels)-1)]
	logger = golog.New(os.Stderr, logLevel)

	guess := (*TMGuess)(nil)
	if args != nil && len(args) == 0 {
		guess = guessTimeMachineBackup()
		if (*guess).Directory != nil {
			args = []string{"/Home/" + path.Join(*guess.Directory, "Home")}
		}
	}

	if err != nil {
		if args == nil {
			fmt.Print(err)
		}
		fmt.Println("Example:")
		argv0 := os.Args[0]
		if len(argv0) > 15 {
			argv0 = "..." + argv0[len(argv0)-15:]
		}
		fmt.Printf("  $ %s /Users/wolever:/Volumes/Backup/Users/wolever\n", argv0)
		fmt.Printf("\n")
		fmt.Printf("Defaults:\n")
		fmt.Printf("  On OS X, REFERENCE_DIR defaults to /Home/ and BACKUP_DIR \n")
		fmt.Printf("  defaults to your most recent Time Machine backup.\n")
		if guess != nil {
			g := *guess
			if g.NoTM {
				fmt.Println("  Time Machine (tmutil) was not found so there will be no default.")
			} else if g.Unmounted {
				fmt.Println("  Your Time Machine backup isn't mounted and will not be used.")
			} else if g.Invalid {
				fmt.Println("  tmutil returned an invalid path (use -vvv for more).")
			} else if g.Directory != nil {
				fmt.Println("  This Time Machine backup will be used: " + *g.Directory)
			}
		}
		fmt.Printf("\n")
		os.Exit(1)
		return
	}

	type Pair struct {
		ref *WalkerItem
		bck *WalkerItem
	}

	pairs := make([]Pair, len(args))
	for idx, backup := range args {
		pair := strings.Split(backup, ":")
		if len(pair) != 2 {
			logger.Errorf("invalid REFERENCE_DIR:BACKUP_DIR pair: %s (hint: /Home/:/Volumes/Backup/Home)", backup)
			os.Exit(1)
		}

		refRoot, err := WalkerItemFromRoot(pair[0])
		if err != nil {
			logger.Error(err)
			os.Exit(1)
		}

		bckRoot, err := WalkerItemFromRoot(pair[1])
		if err != nil {
			logger.Error(err)
			os.Exit(1)
		}

		pairs[idx] = Pair{refRoot, bckRoot}
	}

	for _, pair := range pairs {

		walker := NewDFWalker(pair.ref)

		for {
			refItem, err := walker.Next()
			if err != nil {
				fmt.Println("ERROR:", err)
				logger.Error(err)
				return
			}

			if refItem == nil {
				break
			}

			if refItem.Err() != nil {
				fmt.Println("ASSERITON ERROR: refItem.Err() should not be nil")
				logger.Error(refItem.Err())
				return
			}

			err = check(refItem, pair.bck.GetItem(refItem))
			if err != nil {
				fmt.Printf("%s: %s\n", refItem.RelPath(), err)
			}
		}
	}
}

package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"reflect"
)

var ERR_NOT_DIR = errors.New("not a directory")

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

		if !reflect.DeepEqual(rChunk, bChunk) {
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

func main() {
	refRoot, err := WalkerItemFromRoot("a")
	if err != nil {
		log.Fatal(err)
		return
	}

	bckRoot, err := WalkerItemFromRoot("b")
	if err != nil {
		log.Fatal(err)
		return
	}

	walker := NewDFWalker(refRoot)

	for {
		refItem, err := walker.Next()
		if err != nil {
			fmt.Println("ERROR:", err)
			log.Fatal(err)
			return
		}

		if refItem == nil {
			break
		}

		if refItem.Err() != nil {
			fmt.Println("ASSERITON ERROR: refItem.Err() should not be nil")
			log.Fatal(refItem.Err())
			return
		}

		err = check(refItem, bckRoot.GetItem(refItem))
		if err != nil {
			fmt.Printf("%s: %s\n", refItem.RelPath(), err)
		}
	}
}

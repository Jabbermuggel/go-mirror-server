/*
 * This implements a custom reader for files that are currently being written to disk and the final byte size is known
 * it will implement the io.ReadSeeker interface; see https://pkg.go.dev/io#ReadSeeker
 */

package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

type CustomReadSeeker struct {
	filePath       string
	finalSize      int64
	fileHandler    *os.File
	initialized    bool
	positionInFile int64
}

func (t CustomReadSeeker) WaitForSize(goalSize int64) error {
	// this function blocks until the file is either as large as the final size or exceeds the goalSize
	var stat fs.FileInfo
	var err error
	for {
		stat, err = t.fileHandler.Stat()
		if err != nil {
			return err
		}
		if t.finalSize <= stat.Size() || goalSize <= stat.Size() {
			break
		}
		// you could leave this but that will increase CPU usage dramatically
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func (t CustomReadSeeker) Read(p []byte) (int, error) {
	// "If some data is available but not len(p) bytes, Read conventionally returns what is available instead of waiting for more."
	// I think I will wait for the file to reach necessary size regardless
	pos, err := t.fileHandler.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	err = t.WaitForSize(pos + int64(len(p)))
	if err != nil {
		return 0, err
	}
	return t.fileHandler.Read(p)
}

func (t CustomReadSeeker) Seek(offset int64, whence int) (int64, error) {
	// this gets called by ServeContent in the following situations (I think)
	//   - 0, 2 once to get the file size
	//   - n, 0 to get wherever the transmission is supposed to start.
	// we'll just handle those specifically
	//log.Printf("Trying to seek in file offset %d in mode %d", offset, whence)

	if offset == 0 && whence == io.SeekEnd {
		return t.finalSize, nil
	} else if whence == io.SeekStart {
		// we need to check that the file is already written far enough
		stat, err := t.fileHandler.Stat()
		if err != nil {
			return 0, err
		}
		if stat.Size() < offset {
			return 0, fmt.Errorf("error while trying to acces file: download not progressed far enough")
		}
		return t.fileHandler.Seek(offset, whence)
	} else {
		return 0, fmt.Errorf("not implemented")
	}
}

func (t *CustomReadSeeker) Init(goalSize int64, path string) error {
	var err error
	t.filePath = path
	t.finalSize = goalSize
	t.fileHandler, err = os.OpenFile(t.filePath, os.O_RDONLY, 0755)
	if err != nil {
		return err
	}
	t.initialized = true
	t.positionInFile = 0
	return nil
}

func (t CustomReadSeeker) Close() error {
	return t.fileHandler.Close()
}

/*
 * This implements a custom reader for files that are currently being written to disk and the final byte size is known
 * it will implement the io.ReadSeeker interface; see https://pkg.go.dev/io#ReadSeeker
 */

package main

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"time"
)


type CustomReadSeeker struct {
	filePath 		string
	finalSize		int64
	fileHandler 	*os.File
	initialized		bool
	positionInFile	int64
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
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}


func (t CustomReadSeeker) Read(p []byte) (int, error) {
	// "If some data is available but not len(p) bytes, Read conventionally returns what is available instead of waiting for more."
	// I think I will wait for the file to reach necessary size regardless
	err := t.WaitForSize(t.positionInFile + int64(len(p)))
	if err != nil {
		return 0, err
	}
	log.Printf("Trying to read from file at position %d number of bytes %d", t.positionInFile, len(p))

	t.fileHandler.Seek(t.positionInFile, io.SeekStart)
	return t.fileHandler.Read(p)
}

func (t CustomReadSeeker) Seek(offset int64, whence int) (int64, error) {
	log.Printf("Trying to seek in file offset %d in mode %d", offset, whence)
	// not sure whether we should wait here for the file to be at an appropriate position or just set the offset and wait in the read function. I'll do the first here
	switch whence {
	case io.SeekStart:
		if offset > t.finalSize || offset < 0 {
			return t.positionInFile, fmt.Errorf("invaild Offset: ouside of maximum file dimensions")
		}
		t.positionInFile = offset
	case io.SeekCurrent:
		if offset + t.positionInFile > t.finalSize || offset + t.positionInFile < 0 {
			return t.positionInFile, fmt.Errorf("invaild Offset: ouside of maximum file dimensions")
		}
		t.positionInFile = offset + t.positionInFile
	case io.SeekEnd:
		if offset + t.finalSize > t.finalSize || offset + t.finalSize < 0 {
			return t.positionInFile, fmt.Errorf("invaild Offset: ouside of maximum file dimensions")
		}
		log.Printf("Seeking from the end")
		t.positionInFile = offset + t.finalSize
	default:
		return t.positionInFile, fmt.Errorf("invalid whence value of %v", whence)
	}
	return t.positionInFile, nil
	//return t.positionInFile, fmt.Errorf("unexpected behavior in seek function")
}

func (customReadSeeker *CustomReadSeeker) Init(goalSize int64, path string) error {
	var err error
	customReadSeeker.filePath = path
	customReadSeeker.finalSize = goalSize
	customReadSeeker.fileHandler, err = os.OpenFile(customReadSeeker.filePath, os.O_RDONLY, 0755)
	if err != nil {
		return err
	}
	customReadSeeker.initialized = true
	customReadSeeker.positionInFile = 0
	return nil
}

func (customReadSeeker CustomReadSeeker) Close() error {
	return customReadSeeker.fileHandler.Close()
}
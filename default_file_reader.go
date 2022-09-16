// a wrapper for a normal file handler

package main

import (
	"os"
	"log"
)

type DefaultFileReader struct {
	filePath 		string
	fileHandler 	*os.File
}




func (t DefaultFileReader) Read(p []byte) (int, error) {
	//log.Printf("Trying to read from file at number of bytes %d", len(p))
	return t.fileHandler.Read(p)
}

func (t DefaultFileReader) Seek(offset int64, whence int) (int64, error) {
	log.Printf("Trying to seek in file offset %d in mode %d", offset, whence)
	return t.fileHandler.Seek(offset, whence)
}

func (customReadSeeker *DefaultFileReader) Init(path string) error {
	var err error
	customReadSeeker.filePath = path
	customReadSeeker.fileHandler, err = os.OpenFile(customReadSeeker.filePath, os.O_RDONLY, 0755)
	if err != nil {
		return err
	}
	return nil
}
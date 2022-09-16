package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cavaliergopher/grab/v3"
)

var urlRegex *regexp.Regexp // to get the details of a package (arch, version etc)
const CacheDir = "cache"
const RemoteRepoUrl = "http://127.0.0.1:8000/repo/copy"

type FileInfo struct {
	mu       *sync.Mutex // this is pretty redundant. we already lock the whole map, why add this?
	fullSize int64
}

// A map for files currently being downloaded. The lock mutex is for preventing double access on this map
var (
	files     = make(map[string]FileInfo)
	filesLock sync.Mutex
)

func main() {
	var err error
	//logger = log.New(os.Stdout, "http: ", log.LstdFlags)
	http.HandleFunc("/", handlerWrapper)

	urlRegex, err = regexp.Compile("^/repo/([^/]*)")
	if err != nil {
		log.Fatal(err)
		return
	}

	fmt.Printf("Starting server at port 9000\n")
	if err = http.ListenAndServe(":9000", nil); err != nil {
		log.Fatal(err)
	}
}

func handlerWrapper(w http.ResponseWriter, r *http.Request) {
	if err := handleRequest(w, r); err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusNotFound)
	}
}

func handleRequest(w http.ResponseWriter, req *http.Request) error {
	// get filename we want to serve
	matches := urlRegex.FindStringSubmatch(req.URL.Path)
	if len(matches) == 0 {
		return fmt.Errorf("invalid path")
	}
	fileName := matches[1]

	// create cache directory if needed
	filePath := filepath.Join(CacheDir, fileName)
	if _, err := os.Stat(CacheDir); os.IsNotExist(err) {
		if err := os.MkdirAll(CacheDir, os.ModePerm); err != nil {
			return err
		}
	}
	// requestFromServer tells us that we need to check for the file at the remote server,
	// as it isn't being downloaded currently and either may have changed or flat out doesn't exist in our cache
	stat, err := os.Stat(filePath)
	noFile := err != nil
	//requestFromServer := noFile || forceCheckAtServer(fileName)

	ifLater, _ := http.ParseTime(req.Header.Get("If-Modified-Since"))
	if noFile {
		// ignore If-Modified-Since and download file if it does not exist in the cache
		ifLater = time.Time{}
	} else if stat.ModTime().After(ifLater) {
		ifLater = stat.ModTime()
	}

	if noFile {
		// if the file isn't in cache
		startDownload(fileName, filePath, RemoteRepoUrl+"/"+fileName, ifLater)
		log.Printf("File missing. Starting download and serving file")
		// now basically the same as if the download was already running when we got here
		filesLock.Lock()
		downloadingFileInfo, ok := files[fileName]
		if ok {
			var file CustomReadSeeker
			file.Init(downloadingFileInfo.fullSize, filePath)

			// unlock the lock now early so that other threads won't get blocked during download
			filesLock.Unlock()
			http.ServeContent(w, req, "", time.Now(), file)
			file.Close()

		} else {
			log.Printf("File download just got finished! We can serve the file normally now")
			filesLock.Unlock()
		}
		return nil
		// now send the data during download
	} else if isBeingDownloaded(fileName) {
		// the file is currently in download. thus, we need to piggyback off of that

		filesLock.Lock()
		downloadingFileInfo, ok := files[fileName]
		if ok {
			// file still in download
			log.Printf("File currently being downloaded. Piggy-Back off the existing download")

			var file CustomReadSeeker
			file.Init(downloadingFileInfo.fullSize, filePath)

			// unlock the lock now early so that other threads won't get blocked during download
			filesLock.Unlock()
			http.ServeContent(w, req, "", time.Now(), file)
			file.Close()

		} else {
			log.Printf("File download just got finished! We can serve the file normally now")
			filesLock.Unlock()
		}
	} else if forceCheckAtServer(fileName) {
		// the file is here and not in download but we still need to check since it may have changed
		log.Printf("File existing but may have changed on remote server. Downloading anyways")
	} else {
		// the file is here, not in download and hasn't changed on the server. just serve the cached file
		log.Printf("Serve cached file")
		sendCachedFile(w, req, fileName, filePath)
	}

	return err
}

// I think this function checks whether there was a file requested that can change on the remote mirror, i. e. package databases and such.
func forceCheckAtServer(fileName string) bool {
	// Suffixes for mutable files. We need to check the files modification date at the server.
	forceCheckFiles := []string{".db", ".db.sig", ".files"}

	for _, e := range forceCheckFiles {
		if strings.HasSuffix(fileName, e) {
			return true
		}
	}
	return false
}

// this is a simple function to serve the http content. should actually support byte range download, I am a bit surprised it doesn't seem to.
// filename can actually be empty since it doesn't matter for ServeContent (see docs)
func sendCachedFile(w http.ResponseWriter, req *http.Request, fileName string, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}
	log.Printf("serving cached file %v", filePath)
	http.ServeContent(w, req, fileName, stat.ModTime(), file)
	return nil
}

// checks whether a given file is being downloaded right now by referencing in the downloadingFiles map.
func isBeingDownloaded(fileName string) bool {
	filesLock.Lock()
	defer filesLock.Unlock()
	_, ok := files[fileName]
	return ok
}

// this function starts a download and saves that fact for later usage
func startDownload(fileName string, filePath string, url string, ifModifiedSince time.Time) {
	// protect against double downloads: return if download is in progress or create new download entry

	filesLock.Lock()
	newFileInfo, ok := files[fileName]
	if ok {
		return // download is in progress
	}
	newFileInfo.mu = &sync.Mutex{}

	client := grab.NewClient()
	req, _ := grab.NewRequest(filePath, url)
	req.NoResume = true
	if !ifModifiedSince.IsZero() {
		req.HTTPRequest.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}
	// golang requests compression for all requests except HEAD. some servers return compressed data without Content-Length header info. disable compression as it useless for package data
	req.HTTPRequest.Header.Add("Accept-Encoding", "identity")

	req.AfterCopy = func(response *grab.Response) error {
		// cleaning up the list of downloading items
		filesLock.Lock()
		delete(files, fileName)
		filesLock.Unlock()
		return nil
	}
	resp := client.Do(req)

	newFileInfo.fullSize = resp.Size()
	files[fileName] = newFileInfo
	filesLock.Unlock()

	log.Printf("started grab request, status code %v", resp.HTTPResponse.Status)
}

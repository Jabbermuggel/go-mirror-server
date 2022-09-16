package main

import (
	"fmt"
	"io"
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
const download_rate_limit = 1024 * 1000
const CACHE_DIR = "cache"
const REMOTE_REPO_URL = "http://127.0.0.1:8000/repo/copy"

type downloadingFilesInfo struct {
    mu       *sync.Mutex
	fullSize int64
	progress int64
	modifyProgress	*sync.Mutex
}


// A mutex map for files currently being downloaded. It is used to prevent downloading the same file with concurrent requests
// TODO: would maybe be more elegant with some sort of container?
var (
	downloadingFiles      = make(map[string]downloadingFilesInfo) // this allocates (make) a map which will map strings to *sync.Mutex (mutex-pointers)
	downloadingFilesMutex sync.Mutex
	// this stores info for files that are currently being downloaded
)

func main() {
	var err error
	//logger = log.New(os.Stdout, "http: ", log.LstdFlags)
	http.HandleFunc("/", handlerWrapper)

	urlRegex, err = regexp.Compile("^/repo/([^/]*)")
	if err != nil {
		fmt.Printf("Invalid Regex")
		log.Fatal(err)
		return
	}

	fmt.Printf("Starting server at port 9000\n")
	if error := http.ListenAndServe(":9000", nil); error != nil {
		log.Fatal(error)
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
	filePath := filepath.Join(CACHE_DIR, fileName)
	if _, err := os.Stat(CACHE_DIR); os.IsNotExist(err) {
		if err := os.MkdirAll(CACHE_DIR, os.ModePerm); err != nil {
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
		startDownload(fileName, filePath, REMOTE_REPO_URL + "/" + fileName, ifLater)
		log.Printf("File missing. Starting download")
		// now send the data during download
	} else if isBeingDownloaded(fileName) {
		// the file is currently in download. thus, we need to piggyback off of that

		downloadingFilesMutex.Lock()
		defer downloadingFilesMutex.Unlock()
		downloadingFile, ok := downloadingFiles[fileName]
		if ok {
			// file still in download
			downloadingFile.modifyProgress.Lock()
			log.Printf("File currently being downloaded(%v/%v). Piggy-Back off the existing download", downloadingFile.progress, downloadingFile.fullSize)
			downloadingFile.modifyProgress.Unlock()
		} else {
			log.Printf("File download just got finished!")
		}
	} else if forceCheckAtServer(fileName) {
		// the file is here and not in download but we still need to check since it may have changed
		log.Printf("File existing but may have changed on remote server. Downloading anyways")
	} else {
		// the file is here, not in download and hasn't changed on the server. just serve the cached file
		log.Printf("Serve cached file")
	}

	return err
}

// downloadFileAndSend downloads file from `url`, saves it to the given `localFileName`
// file and sends to `clientWriter` at the same time.
// The function returns whether the function sent the data to client and error if one occurred.
// This is the main thing that needs improving I think
func downloadFileAndSend(url string, filePath string, ifModifiedSince time.Time, clientWriter http.ResponseWriter, request *http.Request) (served bool, err error) {
	// something with timeouts here in the original source, but I don't think it matters too much
	// I don't think we need to protect against double downloads here as 
	client := grab.NewClient()
	req, err := grab.NewRequest(filePath, url)
	req.NoResume = true
	// adding a few headers
	if !ifModifiedSince.IsZero() {
		req.HTTPRequest.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}
	// golang requests compression for all requests except HEAD. some servers return compressed data without Content-Length header info. disable compression as it useless for package data
	req.HTTPRequest.Header.Add("Accept-Encoding", "identity")
	resp := client.Do(req)

	log.Printf("did grab request, status code", resp.HTTPResponse.Status)
	log.Printf("downloading %v", url)

	dummyReader := strings.NewReader("This is a test message")
	_, err = io.Copy(clientWriter, dummyReader)

	// this isn't working
	if err != nil {
		// remove the cached file if download was not successful
		log.Print(err)
		_ = os.Remove(filePath)
		return
	}
	served = true

	// modify the file such that it matches the actual change times 
	if lastModified := resp.HTTPResponse.Header.Get("Last-Modified"); lastModified != "" {
		lastModified, parseErr := http.ParseTime(lastModified)
		err = parseErr
		if err == nil {
			if err = os.Chtimes(filePath, time.Now(), lastModified); err != nil {
				return
			}
		}
	}

	return
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
	downloadingFilesMutex.Lock()
	defer downloadingFilesMutex.Unlock()
	_, ok := downloadingFiles[fileName]
	return ok
}

// this function starts a download and saves that fact for later usage
func startDownload(fileName string, filePath string, url string, ifModifiedSince time.Time) {
	// protect against double downloads: return if download is in progress or create new download entry
	downloadingFilesMutex.Lock()                             
	newFileToDownloadMutex, ok := downloadingFiles[fileName]
	if ok {
		return 	// a download is already in progress
	}
	newFileToDownloadMutex.mu = &sync.Mutex{}
	newFileToDownloadMutex.modifyProgress = &sync.Mutex{}
	newFileToDownloadMutex.mu.Lock()
	newFileToDownloadMutex.modifyProgress.Lock() 					// lock this until we can set good values

	defer func() {
		// once we are done we can unlock and remove the new mutex from the map since it is, in fact, downloaded now (unless that got interrupted)
		newFileToDownloadMutex.mu.Unlock()
		downloadingFilesMutex.Lock()
		delete(downloadingFiles, fileName)
		downloadingFilesMutex.Unlock()
	}()


	// I don't think we need to protect against double downloads here as 
	client := grab.NewClient()
	req, _ := grab.NewRequest(filePath, url)
	req.NoResume = true
	// adding a few headers
	if !ifModifiedSince.IsZero() {
		req.HTTPRequest.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}
	// golang requests compression for all requests except HEAD. some servers return compressed data without Content-Length header info. disable compression as it useless for package data
	req.HTTPRequest.Header.Add("Accept-Encoding", "identity")
	resp := client.Do(req)

	newFileToDownloadMutex.fullSize = resp.Size()
	newFileToDownloadMutex.modifyProgress.Unlock()
	downloadingFiles[fileName] = newFileToDownloadMutex
	downloadingFilesMutex.Unlock()

	log.Printf("did grab request, status code", resp.HTTPResponse.Status)

	// is this ticker necessary? is the time alright?
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	Loop:
	for {
		select {
		case <-t.C:
			//	we update the download progress
			//fmt.Printf("  transferred %v / %v bytes (%.2f%%)\n",
			//	resp.BytesComplete(),
			//	resp.Size,
			//	100*resp.Progress())
			// overwrite this thingy
			downloadingFilesMutex.Lock()
			newFileToDownloadMutex = downloadingFiles[fileName]
			newFileToDownloadMutex.modifyProgress.Lock()
			newFileToDownloadMutex.progress = resp.BytesComplete()
			newFileToDownloadMutex.modifyProgress.Unlock()
			downloadingFiles[fileName] = newFileToDownloadMutex
			downloadingFilesMutex.Unlock()

		case <-resp.Done:
			// download is complete
			break Loop
		}
	}
	log.Printf("Download of file %v done!", fileName)
}
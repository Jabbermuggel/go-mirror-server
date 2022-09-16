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
)

var urlRegex *regexp.Regexp // to get the details of a package (arch, version etc)
const download_rate_limit = 1024 * 1000
const CACHE_DIR = "cache"
const REMOTE_REPO_URL = "http://127.0.0.1:8000/repo/copy"

// A mutex map for files currently being downloaded. It is used to prevent downloading the same file with concurrent requests
// TODO: would maybe be more elegant with some sort of container?
var (
	downloadingFiles      = make(map[string]*sync.Mutex) // this allocates (make) a map which will map strings to *sync.Mutex (mutex-pointers)
	downloadingFilesMutex sync.Mutex
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

	fmt.Printf("Starting server at port 8000\n")
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

	// this is probably a bad way to do it, we'd also need to check whether we are currently already downloading the file
	// but this is the pacoloco way
	stat, err := os.Stat(filePath)
	noFile := err != nil
	requestFromServer := noFile || forceCheckAtServer(fileName)

	if requestFromServer {
		// we first check whether the file is currently being downloaded
		mutexKey := fileName
		downloadingFilesMutex.Lock()                             // lock to indicate we will now access the map
		newFileToDownloadMutex, ok := downloadingFiles[mutexKey] // check if the file is actually currently being downloaded
		if !ok {                                                 // if not put it there; since this is a mutex only we can currently write to the thingy
			// no result; create new mutex and put it in the map
			newFileToDownloadMutex = &sync.Mutex{}
			downloadingFiles[mutexKey] = newFileToDownloadMutex
		}
		downloadingFilesMutex.Unlock()
		newFileToDownloadMutex.Lock() // shouldn't that block until a download is completed
		defer func() {
			// once we are done we can unlock and remove the new mutex from the map since it is, in fact, downloaded now (unless that got interrupted)
			newFileToDownloadMutex.Unlock()
			downloadingFilesMutex.Lock()
			delete(downloadingFiles, mutexKey)
			downloadingFilesMutex.Unlock()
		}()

		// refresh the data in case if the file has been download while we were waiting for the mutex
		// this actually makes a lot of sense I think? can't explain it atm tho.
		stat, err = os.Stat(filePath)
		noFile = err != nil
		requestFromServer = noFile || forceCheckAtServer(fileName)
	}
	var served bool // this var will store whether we have served the file already and if not we will send the cached file later
	if requestFromServer {
		// not sure what this does exactly
		ifLater, _ := http.ParseTime(req.Header.Get("If-Modified-Since"))
		if noFile {
			// ignore If-Modified-Since and download file if it does not exist in the cache
			ifLater = time.Time{}
		} else if stat.ModTime().After(ifLater) {
			ifLater = stat.ModTime()
		}
		// pacoloco would differentiate between repos here and save downloaded files into the database for prefetching. for this minimal example we don't need that
		served, err = downloadFileAndSend(REMOTE_REPO_URL+"/"+fileName, filePath, ifLater, w)
		if err != nil {
			log.Println("Error with downloadFileAndSend:", err)
		}
	}
	// if we haven't already sent the file go do it now (and pacoloco afterwards updates the prefetch DB)
	if !served {
		err = sendCachedFile(w, req, fileName, filePath)
	}
	return err
}

// downloadFileAndSend downloads file from `url`, saves it to the given `localFileName`
// file and sends to `clientWriter` at the same time.
// The function returns whether the function sent the data to client and error if one occurred.
// This is the main thing that needs improving I think
func downloadFileAndSend(url string, filePath string, ifModifiedSince time.Time, clientWriter http.ResponseWriter) (served bool, err error) {
	var req *http.Request
	// something with timeouts here in the original source, but I don't think it matters too much
	req, err = http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}

	if !ifModifiedSince.IsZero() {
		req.Header.Set("If-Modified-Since", ifModifiedSince.UTC().Format(http.TimeFormat))
	}
	// golang requests compression for all requests except HEAD
	// some servers return compressed data without Content-Length header info
	// disable compression as it useless for package data
	req.Header.Add("Accept-Encoding", "identity")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		break
	case http.StatusNotModified:
		// either pacoloco or client has the latest version, no need to redownload it
		return
	default:
		// for most dbs signatures are optional, be quiet if the signature is not found
		// quiet := resp.StatusCode == http.StatusNotFound && strings.HasSuffix(url, ".db.sig")
		err = fmt.Errorf("unable to download url %s, status code is %d", url, resp.StatusCode)
		return
	}

	file, err := os.Create(filePath)
	if err != nil {
		return
	}

	log.Printf("downloading %v", url)
	clientWriter.Header().Set("Content-Length", resp.Header.Get("Content-Length"))
	clientWriter.Header().Set("Content-Type", "application/octet-stream")
	clientWriter.Header().Set("Last-Modified", resp.Header.Get("Last-Modified"))

	// here the magic happens. this code writes the response body we just got directly to both the file on disk as
	// well as the response header of the web server (however that works)
	w := io.MultiWriter(file, clientWriter)
	_, err = io.Copy(w, resp.Body)
	_ = file.Close() // Close the file early to make sure the file modification time is set
	if err != nil {
		// remove the cached file if download was not successful
		log.Print(err)
		_ = os.Remove(filePath)
		return
	}
	served = true

	if lastModified := resp.Header.Get("Last-Modified"); lastModified != "" {
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

// I don't really understand this function
func forceCheckAtServer(fileName string) bool {
	// Suffixes for mutable files. We need to check the files modification date at the server.
	forceCheckFiles := []string{".db", ".db.sig", ".files"}

	for _, e := range forceCheckFiles {
		if strings.HasSuffix(fileName, e) {
			log.Println("ForceCheckAtServer returned true")
			return true
		}
	}
	log.Println("ForceCheckAtServer returned true")
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

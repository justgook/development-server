package main

import (
	"flag"
	"fmt"
	"github.com/evanw/esbuild/pkg/api"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		log.Fatal(err)
	}
}

const (
	// Poll file for changes with this period.
	filePeriod = 500 * time.Millisecond
)

var addr = flag.String("addr", "localhost:8080", "http service address")
var rootDir = flag.String("dir", "./src", "Set the root directory for the server.")

func main() {
	openBrowser("http://localhost:8080")
	flag.Parse()
	log.SetFlags(0)

	http.HandleFunc("/reload", reloadSSE())

	http.HandleFunc("/", transform)

	log.Println("Running at", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

var fileContent = make(map[string][]byte)

func isFileIfModified(filename string, lastMod time.Time) (bool, time.Time) {
	fi, err := os.Stat(filename)
	if err != nil {
		log.Println("isFileIfModified::error", err)
		return false, lastMod
	}
	if !fi.ModTime().After(lastMod) {
		return false, lastMod
	}
	return true, fi.ModTime()
}

func watcher(filename string, lastMod time.Time) {
	fileTicker := time.NewTicker(filePeriod)
	defer func() {
		fileTicker.Stop()
	}()
	for {
		select {
		case <-fileTicker.C:
			var isMod bool
			isMod, lastMod = isFileIfModified(filename, lastMod)
			if _, ok := fileContent[filename]; isMod || !ok {
				delete(fileContent, filename)
				sendFilename(filename)
				log.Println("File changed:" + filename)
				return
			}
		}
	}
}

func transform(w http.ResponseWriter, r *http.Request) {
	p := path.Join("./", *rootDir, r.URL.Path)
	clean := false

	if p == path.Clean(*rootDir) {
		p += "/index.html"
		clean = true
	}
	log.Println("Serving:", p)
	absP, err := filepath.Abs(p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	switch path.Ext(p) {
	case "":
		// UGLY fix for typescript imports without extension
		p += ".ts"
		absP += ".ts"
		fallthrough
	case ".ts":
		code, ok := fileContent[absP]
		if !ok {
			content, err := ioutil.ReadFile(absP)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			result := api.Transform(string(content), api.TransformOptions{
				Loader: api.LoaderTS,
			})
			if len(result.Errors) > 0 {
				http.Error(w, "esbuild fail Transform", http.StatusFailedDependency)
				return
			}
			code = result.Code
			fileContent[absP] = code
			go watcher(absP, time.Now())
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(code)
	case ".elm":
		code, ok := fileContent[absP]
		if !ok {
			f, err := ioutil.TempFile("", "output.*.js")
			if err != nil {
				http.Error(w, err.Error(), http.StatusFailedDependency)
				return
			}
			defer os.Remove(f.Name())
			cmd := exec.Command("elm", "make", absP, "--output="+f.Name())
			stderr, err := cmd.StderrPipe()
			if err != nil {
				log.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				log.Fatal(err)
			}
			slurp, _ := io.ReadAll(stderr)
			fmt.Printf("%s\n", slurp)

			if err := cmd.Wait(); err != nil {
				w.Header().Set("Content-Type", "application/javascript")
				code = []byte("document.body.innerHTML = `<pre>" + strings.Replace(string(slurp), "`", "\\`", -1) + "</pre>`;export const Elm = {}")
				w.Write(code)
				return
			}
			content, err := ioutil.ReadFile(f.Name())
			result :=
				`const scope = {};
` + strings.Replace(string(content), "}(this));", "}(scope));", 1) + `
export const { Elm } = scope;`
			code = []byte(result)
			fileContent[absP] = code
			go watcher(absP, time.Now())
		}
		w.Header().Set("Content-Type", "application/javascript")
		w.Write(code)
	default:
		code, ok := fileContent[absP]
		if !ok {
			code, err = ioutil.ReadFile(absP)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			fileContent[absP] = code
			go watcher(absP, time.Now())
		}
		if clean {
			for k := range fileContent {
				if k != absP {
					delete(fileContent, k)
				}
			}
		}
		w.Write(code)
	}
}

//https://dev.to/mirzaakhena/server-sent-events-sse-server-implementation-with-go-4ck2

func sendFilename(message string) {
	for messageChannel := range messageChannels {
		messageChannel <- []byte(message)
	}
}

var messageChannels = make(map[chan []byte]bool)

func reloadSSE() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// prepare the header
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_messageChannel := make(chan []byte)
		messageChannels[_messageChannel] = true
		// prepare the flusher
		flusher, _ := w.(http.Flusher)
		// trap the request under loop forever
		for {
			select {
			// message will received here and printed
			case message := <-_messageChannel:
				fmt.Fprintf(w, "data:%s\n\n", message)
				flusher.Flush()
			// connection is closed then defer will be executed
			case <-r.Context().Done():
				delete(messageChannels, _messageChannel)
				return
			}
		}
	}
}

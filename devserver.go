package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/evanw/esbuild/pkg/api"
	"github.com/fsnotify/fsnotify"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type Result struct {
	Message []byte
	Error   error
}

type RequestFile struct {
	Name     string
	Response chan Result
}

type convert func(string) ([]byte, error)

var addr = flag.String("addr", "localhost:8080", "http service address")
var rootDir = flag.String("dir", "./src", "Set the root directory for the server.")

func main() {
	//var srv http.Server
	flag.Parse()
	log.SetFlags(0)
	//fmt.Println("parse args:", flag.Args())

	modifiedFile := make(chan string)
	http.HandleFunc("/reload", reloadSSE(modifiedFile))
	http.HandleFunc("/", handle(modifiedFile))

	log.Println("Running at", *addr)
	openBrowser("http://" + *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func createWatcher(add <-chan string, remove <-chan string, modified chan<- string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	done := make(chan bool)
	go func() {
		for {
			select {
			case filename := <-add:
				err = watcher.Add(filename)
				if err != nil {
					log.Fatal(err)
				}
			case filename := <-remove:
				log.Println("unWatch: ", filename)
				err = watcher.Remove(filename)
				if err != nil {
					log.Fatal(err)
				}
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				log.Println("event:", event)
				if event.Op == fsnotify.Remove || event.Op == fsnotify.Rename {
					modified <- event.Name
				} else if event.Op&fsnotify.Write == fsnotify.Write {
					log.Println("modified file:", event.Name)
					modified <- event.Name
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()
	<-done
	log.Println("Watcher Stop")
}

func track(msg string) (string, time.Time) {
	return msg, time.Now()
}

func duration(msg string, start time.Time) {
	log.Printf("%v: %v\n", msg, time.Since(start).Round(time.Millisecond))
}
func handle(modifiedFile chan<- string) http.HandlerFunc {
	staticFilesCn := make(chan RequestFile)
	tsFilesCn := make(chan RequestFile)
	elmFilesCn := make(chan RequestFile)
	watchFile := make(chan string)
	unwatchFile := make(chan string)

	done := make(chan struct{})
	go cacheRead(ioutil.ReadFile, staticFilesCn, done)
	go cacheRead(TransformTypeScript, tsFilesCn, done)
	go cacheRead(TransformElm, elmFilesCn, done)
	go createWatcher(watchFile, unwatchFile, modifiedFile)

	return func(w http.ResponseWriter, r *http.Request) {

		var code []byte
		p := path.Join("./", *rootDir, r.URL.Path)
		if p == path.Clean(*rootDir) {
			p += "/index.html"
		}
		p, err := filepath.Abs(p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		defer duration(track(p))
		switch path.Ext(p) {
		case "":
			// UGLY fix for typescript imports without extension
			p += ".ts"
			fallthrough
		case ".ts":
			code, err = convertFile(tsFilesCn, p)
			w.Header().Set("Content-Type", "application/javascript")
		case ".elm":
			code, err = convertFile(elmFilesCn, p)
			go func() {
				// TODO rewrite to go https://github.com/NoRedInk/find-elm-dependencies
				cmd, b := exec.Command("find-elm-dependencies", p), new(strings.Builder)
				cmd.Stdout = b
				if err := cmd.Run(); err != nil {
					log.Println(err)
				}
				r := regexp.MustCompile(`'(.+)'`)
				matches := r.FindAllString(b.String(), -1)
				for _, element := range matches {
					select {
					case watchFile <- strings.Replace(element, "'", "", -1):
					default:
						fmt.Println("File watcher is dead")
					}
				}
			}()
			w.Header().Set("Content-Type", "application/javascript")
		default:
			code, err = convertFile(staticFilesCn, p)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		select {
		case watchFile <- p:
		default:
			fmt.Println("File watcher is dead")
		}
		w.Write(code)
	}
}

func convertFile(cn chan RequestFile, p string) ([]byte, error) {
	timeout := make(chan bool, 1)
	go func() {
		time.Sleep(2 * time.Second)
		timeout <- true
	}()
	response := RequestFile{p, make(chan Result)}
	cn <- response
	select {
	case result := <-response.Response:
		close(response.Response)
		if result.Error != nil {
			return nil, result.Error
		}
		return result.Message, nil
	case <-timeout:
		return nil, errors.New("file converting timeout")
	}
}

func cacheRead(transform convert, r chan RequestFile, done chan struct{}) {
	var fileContent = make(map[string][]byte)
	for {
		select {
		case msg1 := <-r:
			p := msg1.Name
			content, ok := fileContent[p]
			var err error
			if !ok {
				content, err = transform(p)
			}
			msg1.Response <- Result{content, err}
		case <-done:
			return
		}
	}
}
func TransformTypeScript(p string) ([]byte, error) {
	content, err := ioutil.ReadFile(p)
	if err != nil {
		return nil, err
	}
	result := api.Transform(string(content), api.TransformOptions{
		Loader: api.LoaderTS,
	})
	if len(result.Errors) > 0 {
		return nil, errors.New("esbuild Got errors")
	}

	return result.Code, nil
}

func TransformElm(p string) ([]byte, error) {
	f, err := ioutil.TempFile("", "output.*.js")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	cmd := exec.Command("elm", "make", p, "--output="+f.Name())

	stderr, err := cmd.StderrPipe()
	if _, err2 := cmd.StdoutPipe(); err != nil || err2 != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	slurp, _ := io.ReadAll(stderr)
	if len(slurp) > 0 {
		fmt.Printf("%s\n", slurp)
		return []byte("document.body.innerHTML = `<pre>" + strings.Replace(string(slurp), "`", "\\`", -1) + "</pre>`;export const Elm = {}"), err
	}

	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	content, err := ioutil.ReadFile(f.Name())
	if err != nil {
		return nil, err
	}
	return []byte(`const scope = {};` + strings.Replace(string(content), "}(this));", "}(scope));", 1) + `export const { Elm } = scope;`), nil
}

//https://dev.to/mirzaakhena/server-sent-events-sse-server-implementation-with-go-4ck2
func reloadSSE(broadcast <-chan string) http.HandlerFunc {
	channels := make(map[chan []byte]bool)
	channelsAdd := make(chan chan []byte)

	go func() {
		for {
			select {
			case message := <-broadcast:
				for channel := range channels {
					channel <- []byte(message)
				}
			case channel := <-channelsAdd:
				channels[channel] = true
			}
		}
	}()
	return func(w http.ResponseWriter, r *http.Request) {
		// prepare the header
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		channel := make(chan []byte)
		channelsAdd <- channel
		// prepare the flusher
		flusher, _ := w.(http.Flusher)
		// trap the request under loop forever
		for {
			select {
			// message will received here and printed
			case message := <-channel:
				fmt.Fprintf(w, "data:%s\n\n", message)
				flusher.Flush()
			// connection is closed then defer will be executed
			case <-r.Context().Done():
				delete(channels, channel)
				close(channel)
				return
			}
		}
	}
}

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

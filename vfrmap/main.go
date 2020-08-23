package main

//go:generate go-bindata -pkg main -o bindata.go -modtime 1 -prefix html html

// build: GOOS=windows GOARCH=amd64 go build -o vfrmap.exe github.com/lian/msfs2020-go/vfrmap

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lian/msfs2020-go/simconnect"
	"github.com/lian/msfs2020-go/vfrmap/websockets"
)

type Report struct {
	simconnect.RecvSimobjectDataByType
	Title         [256]byte `name:"TITLE"`
	Altitude      float64   `name:"INDICATED ALTITUDE" unit:"feet"` // PLANE ALTITUDE or PLANE ALT ABOVE GROUND
	Latitude      float64   `name:"PLANE LATITUDE" unit:"degrees"`
	Longitude     float64   `name:"PLANE LONGITUDE" unit:"degrees"`
	Heading       float64   `name:"PLANE HEADING DEGREES TRUE" unit:"degrees"`
	Airspeed      float64   `name:"AIRSPEED INDICATED" unit:"knot"`
	VerticalSpeed float64   `name:"VERTICAL SPEED" unit:"ft/min"`
	Flaps         float64   `name:"TRAILING EDGE FLAPS LEFT ANGLE" unit:"degrees"`
	Trim          float64   `name:"ELEVATOR TRIM PCT" unit:"percent"`
	RudderTrim    float64   `name:"RUDDER TRIM PCT" unit:"percent"`
}

func (r *Report) RequestData(s *simconnect.SimConnect) {
	defineID := s.GetDefineID(r)
	requestID := defineID
	s.RequestDataOnSimObjectType(requestID, defineID, 0, simconnect.SIMOBJECT_TYPE_USER)
}

var buildVersion string
var buildTime string

var showVersion bool
var verbose bool
var httpListen string
var mapApiKeyDefault string
var mapApiKey string

func main() {
	flag.BoolVar(&showVersion, "v", false, "version")
	flag.BoolVar(&verbose, "verbose", false, "verbose output")
	flag.StringVar(&httpListen, "listen", "0.0.0.0:9000", "http listen")
	flag.StringVar(&mapApiKey, "api-key", "", "gmap api-key")
	flag.Parse()

	if showVersion {
		fmt.Printf("version: %s (%s)\n", buildVersion, buildTime)
		return
	}

	if mapApiKey == "" {
		mapApiKey = mapApiKeyDefault
	}

	exitSignal := make(chan os.Signal, 1)
	signal.Notify(exitSignal, os.Interrupt, syscall.SIGTERM)
	exePath, _ := os.Executable()

	ws := websockets.New()

	s, err := simconnect.New("VFR Map")
	if err != nil {
		panic(err)
	}
	fmt.Println("Connected to Flight Simulator!")

	report := &Report{}
	err = s.RegisterDataDefinition(report)
	if err != nil {
		panic(err)
	}

	report.RequestData(s)

	/*
		fmt.Println("SubscribeToSystemEvent")
		eventSimStartID := simconnect.DWORD(0)
		s.SubscribeToSystemEvent(eventSimStartID, "SimStart")
	*/

	go func() {
		for {
			ppData, r1, err := s.GetNextDispatch()

			if r1 < 0 {
				if uint32(r1) == simconnect.E_FAIL {
					// skip error, means no new messages?
					continue
				} else {
					panic(fmt.Errorf("GetNextDispatch error: %d %s", r1, err))
				}
			}

			recvInfo := *(*simconnect.Recv)(ppData)
			//fmt.Println(ppData, pcbData, recvInfo)

			switch recvInfo.ID {
			case simconnect.RECV_ID_EXCEPTION:
				recvErr := *(*simconnect.RecvException)(ppData)
				fmt.Printf("SIMCONNECT_RECV_ID_EXCEPTION %#v\n", recvErr)

			case simconnect.RECV_ID_OPEN:
				recvOpen := *(*simconnect.RecvOpen)(ppData)
				fmt.Println("SIMCONNECT_RECV_ID_OPEN", fmt.Sprintf("%s", recvOpen.ApplicationName))
				//spew.Dump(recvOpen)
			case simconnect.RECV_ID_EVENT:
				recvEvent := *(*simconnect.RecvEvent)(ppData)
				fmt.Println("SIMCONNECT_RECV_ID_EVENT")
				//spew.Dump(recvEvent)

				switch recvEvent.EventID {
				//case eventSimStartID:
				//	s.RequestDataOnSimObjectType(requestID, defineID, 0, simconnect.SIMOBJECT_TYPE_USER)
				default:
					fmt.Println("unknown SIMCONNECT_RECV_ID_EVENT", recvEvent.EventID)
				}

			case simconnect.RECV_ID_SIMOBJECT_DATA_BYTYPE:
				recvData := *(*simconnect.RecvSimobjectDataByType)(ppData)
				//fmt.Println("SIMCONNECT_RECV_SIMOBJECT_DATA_BYTYPE")

				switch recvData.RequestID {
				case s.DefineMap["Report"]:
					report = (*Report)(ppData)

					if verbose {
						fmt.Printf("REPORT: %#v\n", report)
					}

					ws.Broadcast(map[string]interface{}{
						"latitude":       report.Latitude,
						"longitude":      report.Longitude,
						"altitude":       fmt.Sprintf("%.0f", report.Altitude),
						"heading":        int(report.Heading),
						"airspeed":       fmt.Sprintf("%.0f", report.Airspeed),
						"vertical_speed": fmt.Sprintf("%.0f", report.VerticalSpeed),
						"flaps":          fmt.Sprintf("%.0f", report.Flaps),
						"trim":           fmt.Sprintf("%.1f", report.Trim),
						"rudder_trim":    fmt.Sprintf("%.1f", report.RudderTrim),
					})

					report.RequestData(s)
				}

			default:
				fmt.Println("recvInfo.ID unknown", recvInfo.ID)
			}

			time.Sleep(100 * time.Millisecond)
		}
	}()

	go func() {
		app := func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			w.Header().Set("Content-Type", "text/html")

			filePath := filepath.Join(filepath.Dir(exePath), "index.html")

			var buf []byte
			if _, err = os.Stat(filePath); os.IsNotExist(err) {
				buf = MustAsset(filepath.Base(filePath))
			} else {
				fmt.Println("use local", filePath)
				//http.ServeFile(w, r, filePath)
				buf, _ = ioutil.ReadFile(filePath)
			}

			buf = bytes.Replace(buf, []byte("{{API_KEY}}"), []byte(mapApiKey), -1)
			w.Write(buf)
		}

		http.HandleFunc("/ws", ws.Serve)
		http.HandleFunc("/", app)
		//http.Handle("/", http.FileServer(http.Dir(".")))

		err := http.ListenAndServe(httpListen, nil)
		if err != nil {
			panic(err)
		}
	}()

	for {
		select {

		case <-exitSignal:
			fmt.Println("exiting..")
			if err = s.Close(); err != nil {
				panic(err)
			}
			os.Exit(0)

		case _ = <-ws.NewConnection:
			// drain and skip

		case _ = <-ws.ReceiveMessages:
			// drain and skip

		}
	}
}

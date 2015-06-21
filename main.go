// Runs a node on a random UDP port that attempts to collect 10 peers for an
// infohash, then keeps running as a passive DHT node.
//
// IMPORTANT: if the UDP port is not reachable from the public internet, you
// may see very few results.
//
// To collect 10 peers, it usually has to contact some 1k nodes. It's much easier
// to find peers for popular infohashes. This process is not instant and should
// take a minute or two, depending on your network connection.
//
//
// There is a builtin web server that can be used to collect debugging stats
// from http://localhost:8711/debug/vars.
package main

import (
	"flag"
	"fmt"
	"bufio"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"errors"
	"crypto/sha1"
	"encoding/base64"
	"hash/crc32"
//	"net/http"

	log "github.com/golang/glog"
	"github.com/nictuku/dht"
//	"golang.org/x/crypto/ssh/terminal"
)

const (
	httpPortTCP  = 8711
	messagesPort = 3300
	packFmt      = "%s\n%d\n%s\n%d\n%s"
)

func main() {
	optPort := flag.Int("port", messagesPort, "port to listen")
	optDstPort := flag.Int("dstport", *optPort, "destination port")
	optDhtPort := flag.Int("dhtport", 0, "dhp port to use")
	optNodes := flag.String("nodes", "", "nodes to ping")
	optNik := flag.String("nik", "nik", "nikname")
	optTheme := flag.String("room", "chatik", "room_id_without_spaces")

	flag.Parse()
	// To see logs, use the -logtostderr flag and change the verbosity with
	// -v 0 (less verbose) up to -v 5 (more verbose).
	//	if len(flag.Args()) != 2 {
	//		fmt.Fprintf(os.Stderr, "Usage: %v <nikname> <chatname>", os.Args[0])
	//		flag.PrintDefaults()
	//		os.Exit(1)
	//	}

	// For debugging.
	//	go http.ListenAndServe(fmt.Sprintf(":%d", httpPortTCP), nil)

	//	oldState, err := terminal.MakeRaw(0);
	//	if err != nil {
	//		log.Errorf("Terminal initialization error: ", err)
	//		panic(err)
	//	}
	//	defer terminal.Restore(0, oldState);
	//
	//	t := terminal.NewTerminal(os.Stdin, "$ ")

	conf := new(Config)
	conf.port = *optPort
	conf.dstPort = *optDstPort
	conf.dhtPort = *optDhtPort
	conf.startPing = *optNodes

	app, err := NewApp(*optNik, conf)
	if err != nil {
		log.Errorf("App init error: ", err)
		panic(err)
	}
	app.MakeChat(*optTheme)

	app.cli()

	app.Stop()
}

type Config struct {
	port      int
	dstPort   int
	dhtPort   int
	startPing string
}
type App struct {
	nik          string
	rooms        map[string]*ChatRoom
	conn         *net.UDPConn
	dht          *dht.DHT
	stopGrabber  chan bool
	stopListener chan bool
	stopCli      chan bool
	gui          GUI
	msgid        int
	conf         Config
}
type Message struct {
	theme  string
	id     int
	author string
	time   time.Time
	text   string
}
type ChatRoom struct {
	theme      string
	hist       map[uint32]*Message
	mailbox    chan *Message
	newPeers   chan string
	postoffice chan *Message
	stop       chan bool
	peers      map[string]time.Time
}
type GUI struct {
	active string
}

func (app *App) cli() {
	log.V(1).Infof("Start cli")
	bio := bufio.NewReader(os.Stdin)
	if app.gui.active == "" {
		if len(app.rooms) == 0 {
			panic("no chants")
		}
		for k := range app.rooms {
			app.gui.active = k
			break
		}
	}
	chat := app.rooms[app.gui.active]
	for {
		fmt.Print("$ ")
		line, _, err := bio.ReadLine()
		if err != nil {
			return
		}
		m := app.NewMessage(chat.theme, string(line))
		chat.postoffice <- m
	}
}

func NewApp(nik string, conf *Config) (*App, error) {
	log.V(1).Infoln("starting new App")

	var err error
	app := new(App)

	app.conf = *conf

	app.nik = nik
	app.stopGrabber = make(chan bool)
	app.stopListener = make(chan bool)
	app.rooms = make(map[string]*ChatRoom)
	app.msgid = 0

	dhtconf := dht.NewConfig()
	dhtconf.Port = conf.dhtPort
	app.dht, err = dht.New(dhtconf)
	if err != nil {
		return nil, err
	}

	err = app.listener()
	if err != nil {
		return nil, err
	}

	go app.dht.Run()
	go app.grabPeers()

	fmt.Fprintf(os.Stderr, "dht port %d\n", app.dht.Port())
	for _, addr := range strings.Split(conf.startPing, ",") {
		if addr == "" {
			continue
		}
		fmt.Fprintf(os.Stderr, "ping node: [%s]\n", addr)
		app.dht.AddNode(addr)
	}

	return app, nil
}
func (app *App) Stop() {
	app.conn.Close()
	app.stopGrabber <- true
	app.stopListener <- true
}

func (app *App) MakeChat(theme string) {
	log.V(1).Infof("make new Chat [%s]\n", theme)

	r := new(ChatRoom)
	r.theme = sha1sum(theme)
	r.hist = make(map[uint32]*Message)
	r.mailbox = make(chan *Message)
	r.newPeers = make(chan string)
	r.postoffice = make(chan *Message)
	r.stop = make(chan bool)
	r.peers = make(map[string]time.Time)

	app.rooms[r.theme] = r

	go app.spamer(r)
}
func (app *App) findPeers(room *ChatRoom) {
	ihash, err := dht.DecodeInfoHash(room.theme)
	if err != nil {
		log.Errorf("Error parse chat theme: ", err)
		// TODO
	}
	//	fmt.Fprintf(os.Stderr, "req peers: %s\n", room.theme)
	app.dht.PeersRequest(string(ihash), true)
}
func (app *App) spamer(room *ChatRoom) {
	app.findPeers(room)
	ticker := time.NewTicker(10 * time.Second)
	for {
		select {
		case p := <-room.newPeers:
			room.peers[p] = time.Now()
			// TODO say gui something
		case m := <-room.postoffice:
			room.hist[m.hash()] = m

			app.sendMsg(m, &room.peers)
		case m := <-room.mailbox:
			room.hist[m.hash()] = m

			// TODO say to gui something
			// TODO for now print messages here
			fmt.Printf("[%s] %s: %s\n", m.time, m.author, m.text)
		case <-ticker.C:
			app.findPeers(room)

			now := time.Now()
		//	for addr, lastSeen := range room.peers {
		//		if now.Sub(lastSeen) > 60*time.Minute {
		//			delete(room.peers, addr)
		//		}
		//	}
			fmt.Fprintf(os.Stderr, "Send spam: %d msgs to %d receivers\n", len(room.hist), len(room.peers));
			for _, m := range room.hist {
				if now.Sub(m.time) > 24*time.Hour {
					continue
				}
				app.sendMsg(m, &room.peers)
			}

			// TODO need i recreate ticker?
		case <-room.stop:
			return
		}
	}
}

func (app *App) grabPeers() {
	for {
		select {
		case r := <-app.dht.PeersRequestResults:
			for th_code, peers := range r {
				theme := fmt.Sprintf("%x", th_code)
				for _, x := range peers {
					if room, ok := app.rooms[string(theme)]; ok {
						dht_addr := dht.DecodePeerAddress(x)

						log.V(1).Infof("found new peer: ", dht_addr)
						fmt.Printf("new peer: %s\n", dht_addr)

						re := regexp.MustCompile("\\d+\\.\\d+\\.\\d+\\.\\d+")
						ip := re.FindString(dht_addr)
						dest := ip + ":" + strconv.Itoa(app.conf.dstPort)
						room.newPeers <- dest
					}
				}
			}
		case <-app.stopGrabber:
			log.V(2).Infoln("stop grabber. send stops to chatRooms")
			for _, room := range app.rooms {
				room.stop <- true
			}
			log.V(2).Infoln("grabber stopped")
			return
		}
	}
}

func (app *App) sendMsg(m *Message, peers *map[string]time.Time) {
	for peer := range *peers {
		app.sendMsgOne(m, peer)
	}
}
func (app *App) sendMsgOne(m *Message, peer string){
	buf := app.packMessage(m)
	addr, err := net.ResolveUDPAddr("udp4", peer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "send message error %s\n", err)
	}
	app.conn.WriteTo(buf, addr)
}
func (app *App) receiveMsg(buf []byte, addr *net.UDPAddr) error {
	msg := app.unpackMessage(buf)

	if m, ok := app.rooms[msg.theme]; ok {
		m.mailbox <- msg
		return nil
	} else {
		log.V(3).Infof("Recived message to unexisted chat [%s]\n", msg.theme)
		return errors.New("bad udp package format")
	}
}

func (app *App) listener() (err error) {
	addr := net.UDPAddr{Port: app.conf.port}
	conn, err := net.ListenUDP("udp4", &addr)
	if err != nil {
		log.Error("message listener binding failed: ", err)
		return err
	}
	log.V(1).Info("message listener started")
	app.conn = conn

	go func() {
		defer func() {
			log.V(1).Info("message listener closed")
		}()

		buf := make([]byte, 4096)
		for {
			select {
			case <-app.stopListener:
				log.V(2).Infoln("listener stopped")
				return
			default:
			}
			buf = buf[0:cap(buf)]

			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				log.V(3).Infof("UDP read error: ", err)
				continue
			}

			err = app.receiveMsg(buf[0:n], addr)

			if err != nil {
				fmt.Fprintf(os.Stderr, "received from [%s]  %d bytes\n", addr, n)
			}
		}
	}()

	return nil
}

func (app *App) NewMessage(theme, text string) *Message {
	app.msgid += 1
	m := new(Message)
	m.theme = theme
	m.id = app.msgid
	m.author = app.nik
	m.time = time.Now()
	m.text = text

	return m
}
func (app *App) packMessage(m *Message) []byte {
	data := []byte(m.text)
	code := base64.StdEncoding.EncodeToString(data)
	s := fmt.Sprintf(packFmt, m.theme, m.id, m.author, m.time.Unix(), code)
	buf := []byte(s)

	return buf
}
func (app *App) unpackMessage(buf []byte) *Message {
	s := string(buf)
	m := new(Message)
	var t int64
	var code string

	fmt.Sscanf(s, packFmt, &m.theme, &m.id, &m.author, &t, &code)

	m.time = time.Unix(t, 0)
	str, err := base64.StdEncoding.DecodeString(code)
	if err != nil {
		log.V(2).Info("message text body decode err: ", err)
	}
	m.text = string(str)

	return m
}
func (m *Message) hash() uint32 {
	buf := []byte(m.theme + m.author + string(m.id) + string(m.time.Unix()))
	return crc32.ChecksumIEEE(buf)
}
func sha1sum(s string) string {
	hasher := sha1.New()
	hasher.Write([]byte(s))
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

// Command drda-sniff is a transparent TCP proxy that decodes and logs
// the DRDA data stream structures (DSS) passing through it in both
// directions. Point any Db2 client at the listen address to see the
// code points, correlation ids, chaining flags and (optionally) payload
// hex of every message.
//
//	go run ./cmd/drda-sniff -listen :50001 -target localhost:50000 -hex
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"

	"github.com/gizmodata/adbc-driver-db2/internal/ddm"
)

func main() {
	listen := flag.String("listen", ":50001", "address to listen on")
	target := flag.String("target", "localhost:50000", "Db2 server address")
	hexDump := flag.Bool("hex", false, "dump payload bytes")
	maxHex := flag.Int("maxhex", 512, "max payload bytes to dump per DSS")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("drda-sniff: listening on %s, forwarding to %s", *listen, *target)
	var mu sync.Mutex
	connID := 0
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		connID++
		go func(id int, client net.Conn) {
			defer client.Close()
			server, err := net.Dial("tcp", *target)
			if err != nil {
				log.Printf("[%d] dial %s: %v", id, *target, err)
				return
			}
			defer server.Close()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); pump(id, "C->S", client, server, &mu, *hexDump, *maxHex) }()
			go func() { defer wg.Done(); pump(id, "S->C", server, client, &mu, *hexDump, *maxHex) }()
			wg.Wait()
			log.Printf("[%d] closed", id)
		}(connID, c)
	}
}

// pump copies src→dst while decoding DSSs from a tee of the stream.
func pump(id int, dir string, src, dst net.Conn, mu *sync.Mutex, hexDump bool, maxHex int) {
	pr, pw := io.Pipe()
	go func() {
		br := bufio.NewReaderSize(pr, 1<<20)
		for {
			d, err := ddm.ReadDSS(br)
			if err != nil {
				if err != io.EOF {
					log.Printf("[%d] %s decode: %v", id, dir, err)
				}
				return
			}
			mu.Lock()
			flags := ""
			if d.Chained {
				flags += " chained"
			}
			if d.SameCorrelator {
				flags += " same-corr"
			}
			fmt.Fprintf(os.Stdout, "[%d] %s %-10s type=%d corr=%d len=%d%s\n", id, dir, d.CodePoint, d.Type, d.CorrelationID, len(d.Payload), flags)
			if hexDump {
				p := d.Payload
				if len(p) > maxHex {
					p = p[:maxHex]
				}
				fmt.Fprintf(os.Stdout, "      % X\n", p)
			}
			mu.Unlock()
		}
	}()
	_, _ = io.Copy(io.MultiWriter(dst, pw), src)
	pw.Close()
	if tc, ok := dst.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

package agent

import (
	"bufio"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

// handleStream reads one forwarded HTTP request from stream, replays it
// against localAddr, and writes the response back. If the response is a
// protocol upgrade (WebSocket), it falls through to raw bidirectional
// copying for the remaining lifetime of the connection.
func handleStream(stream *yamux.Stream, localAddr string) {
	defer stream.Close()
	start := time.Now()

	streamR := bufio.NewReader(stream)
	req, err := http.ReadRequest(streamR)
	if err != nil {
		if err != io.EOF {
			log.Printf("read forwarded request failed: %v", err)
		}
		return
	}

	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		log.Printf("dial local %s failed: %v", localAddr, err)
		writeBadGateway(stream)
		return
	}
	defer localConn.Close()

	if err := req.Write(localConn); err != nil {
		log.Printf("write request to local server failed: %v", err)
		writeBadGateway(stream)
		return
	}

	localR := bufio.NewReader(localConn)
	resp, err := http.ReadResponse(localR, req)
	if err != nil {
		log.Printf("read response from local server failed: %v", err)
		writeBadGateway(stream)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusSwitchingProtocols {
		passthroughUpgrade(stream, resp, localR, localConn)
		return
	}

	if err := resp.Write(stream); err != nil {
		log.Printf("write response to edge failed: %v", err)
		return
	}
	log.Printf("%s %s -> %d (%s)", req.Method, req.URL.Path, resp.StatusCode, time.Since(start).Round(time.Millisecond))
}

func passthroughUpgrade(stream *yamux.Stream, resp *http.Response, localR *bufio.Reader, localConn net.Conn) {
	if err := resp.Write(stream); err != nil {
		log.Printf("write upgrade response failed: %v", err)
		return
	}

	if n := localR.Buffered(); n > 0 {
		buf := make([]byte, n)
		if _, err := io.ReadFull(localR, buf); err != nil {
			return
		}
		if _, err := stream.Write(buf); err != nil {
			return
		}
	}

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(localConn, stream)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(stream, localR)
		errc <- err
	}()
	<-errc
	log.Printf("websocket connection closed")
}

func writeBadGateway(w io.Writer) {
	const msg = "liveurl: could not reach local server\n"
	resp := &http.Response{
		StatusCode:    http.StatusBadGateway,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:          io.NopCloser(strings.NewReader(msg)),
		ContentLength: int64(len(msg)),
	}
	resp.Write(w)
}

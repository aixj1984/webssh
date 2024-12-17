package webssh

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/aymanbagabas/go-pty"
	"github.com/gorilla/websocket"
)

const (
	MsgData   = '1'
	MsgResize = '2'
)

type Turn struct {
	Pty      pty.Pty
	Cmd      *pty.Cmd
	WsConn   *websocket.Conn
	Recorder *Recorder
}

type Resize struct {
	Columns int `json:"columns"`
	Rows    int `json:"rows"`
}

// NewTurn initializes a new Turn with a pseudo-terminal
func NewTurn(wsConn *websocket.Conn, command string, args []string, recorder *Recorder) (*Turn, error) {
	// Create a new PTY
	ptyInstance, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create PTY: %v", err)
	}

	// Create the command to run
	cmd := ptyInstance.Command(command, args...)
	if err := cmd.Start(); err != nil {
		ptyInstance.Close()
		return nil, fmt.Errorf("failed to start command: %v", err)
	}

	turn := &Turn{
		Pty:      ptyInstance,
		Cmd:      cmd,
		WsConn:   wsConn,
		Recorder: recorder,
	}

	// Start listening for output
	go turn.pipeOutput()

	return turn, nil
}

func (t *Turn) pipeOutput() {
	buffer := make([]byte, 4096)
	for {
		n, err := t.Pty.Read(buffer)
		if err != nil {
			if errors.Is(err, os.ErrClosed) {
				log.Println("PTY closed")
			} else {
				log.Printf("Error reading from PTY: %v", err)
			}
			t.Close() // Close the connection gracefully when reading from PTY fails
			return
		}

		data := buffer[:n]
		// Send output to WebSocket
		if err := t.WsConn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			log.Printf("Error writing to WebSocket: %v", err)
			t.Close() // Close the connection if WebSocket writing fails
			return
		}

		// Record output if recording is enabled
		if t.Recorder != nil {
			t.Recorder.Lock()
			t.Recorder.WriteData(OutPutType, string(data))
			t.Recorder.Unlock()
		}
	}
}

func (t *Turn) LoopRead(logBuff *bytes.Buffer, ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return errors.New("LoopRead exit")
		default:
			_, wsData, err := t.WsConn.ReadMessage()
			if err != nil {
				log.Printf("Error reading WebSocket message: %v", err)
				return fmt.Errorf("reading WebSocket message err: %s", err)
			}

			// Decode the WebSocket message
			body := decode(wsData[1:])
			switch wsData[0] {
			case MsgResize:
				// Handle window resize
				var args Resize
				err := json.Unmarshal(body, &args)
				if err != nil {
					log.Printf("Failed to unmarshal resize message: %v", err)
					return fmt.Errorf("failed to unmarshal resize message: %v", err)
				}
				if args.Columns > 0 && args.Rows > 0 {
					if err := t.Pty.Resize(int(args.Rows), int(args.Columns)); err != nil {
						log.Printf("Failed to resize PTY: %v", err)
						return fmt.Errorf("failed to resize PTY: %v", err)
					}
				}

			case MsgData:
				// Write data to pseudo-terminal
				if _, err := t.Pty.Write(body); err != nil {
					log.Printf("PTY write error: %s", err)
					return fmt.Errorf("PTY write err: %s", err)
				}

				// Write data to log buffer
				if _, err := logBuff.Write(body); err != nil {
					log.Printf("Log buffer write error: %s", err)
					return fmt.Errorf("logBuff write err: %s", err)
				}

				// Record input if recording is enabled
				if t.Recorder != nil {
					t.Recorder.Lock()
					t.Recorder.WriteData(InputType, string(body))
					t.Recorder.Unlock()
				}
			}
		}
	}
}

func (t *Turn) CmdWait() error {
	return t.Cmd.Wait()
}

// Close terminates the pseudo-terminal and cleans up resources
func (t *Turn) Close() error {
	if err := t.Cmd.Process.Kill(); err != nil {
		return err
	}
	t.Pty.Close()
	return t.WsConn.Close()
}

func decode(p []byte) []byte {
	data, _ := base64.StdEncoding.DecodeString(string(p))
	return data
}

func encode(p []byte) []byte {
	encodeToString := base64.StdEncoding.EncodeToString(p)
	return []byte(encodeToString)
}
